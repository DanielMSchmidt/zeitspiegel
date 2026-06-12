package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/window"
)

// ---- fakes -----------------------------------------------------------------

type fakeStatus struct{ st httpapi.Status }

func (f *fakeStatus) Status() httpapi.Status { return f.st }

func defaultStatus() httpapi.Status {
	return httpapi.Status{
		DelayS: 2, FPS: 60, Resolution: "1280x720",
		Buffer:       httpapi.BufferStatus{CapacityS: 120, FilledS: 30, Bytes: 1 << 20},
		MinLatencyMS: 80, UptimeS: 12,
	}
}

type fakeClip struct {
	err  error
	path string
	dur  time.Duration
	got  struct {
		n      time.Duration
		format string
	}
	cleaned bool
}

func (f *fakeClip) ExportClip(_ context.Context, n time.Duration, format string) (httpapi.Clip, error) {
	f.got.n, f.got.format = n, format
	if f.err != nil {
		return httpapi.Clip{}, f.err
	}
	return httpapi.Clip{Path: f.path, Duration: f.dur, Cleanup: func() { f.cleaned = true }}, nil
}

type fakeStore struct{ r config.Runtime }

func (f *fakeStore) Current() config.Runtime { return f.r }
func (f *fakeStore) Apply(p config.Patch) (config.Runtime, error) {
	r, err := f.r.WithPatch(p)
	if err != nil {
		return config.Runtime{}, err
	}
	f.r = r
	return r, nil
}

func newServer(t *testing.T, mod func(*httpapi.Deps)) *httptest.Server {
	t.Helper()
	e := engine.New(ringbuf.New(time.Minute, 1<<20))
	e.SetDelay(2 * time.Second)
	deps := httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: &fakeStatus{st: defaultStatus()},
		Delay:  e,
		Clip:   &fakeClip{path: "unused", dur: 10 * time.Second},
		Config: &fakeStore{r: config.Default().Runtime()},
	}
	if mod != nil {
		mod(&deps)
	}
	srv := httptest.NewServer(httpapi.New(deps))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// ---- FR-8: status schema ----------------------------------------------------

func TestStatusSchema(t *testing.T) {
	srv := newServer(t, nil)
	resp := do(t, srv, "GET", "/api/v1/status", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"delay_s", "fps", "resolution", "buffer", "dropped_frames", "min_latency_ms", "warming_up", "uptime_s"} {
		if _, ok := m[k]; !ok {
			t.Errorf("status missing key %q", k)
		}
	}
	buf, ok := m["buffer"].(map[string]any)
	if !ok {
		t.Fatalf("buffer is %T", m["buffer"])
	}
	for _, k := range []string{"capacity_s", "filled_s", "bytes"} {
		if _, ok := buf[k]; !ok {
			t.Errorf("buffer missing key %q", k)
		}
	}
}

// ---- UT-9: table-driven validation ⇒ 200/422 (FR-11) ------------------------

func TestValidation(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"delay ok", "PUT", "/api/v1/delay", `{"seconds": 4.0}`, 200},
		{"delay zero ok", "PUT", "/api/v1/delay", `{"seconds": 0}`, 200},
		{"delay max ok", "PUT", "/api/v1/delay", `{"seconds": 120}`, 200},
		{"delay negative", "PUT", "/api/v1/delay", `{"seconds": -1}`, 422},
		{"delay over capacity", "PUT", "/api/v1/delay", `{"seconds": 120.1}`, 422},
		{"delay bad json", "PUT", "/api/v1/delay", `{"seconds": "x"}`, 422},
		{"delay empty body", "PUT", "/api/v1/delay", ``, 422},
		{"clip ok", "GET", "/api/v1/clip?seconds=10", "", 200},
		{"clip with format", "GET", "/api/v1/clip?seconds=10&format=mjpeg", "", 200},
		{"clip zero", "GET", "/api/v1/clip?seconds=0", "", 422},
		{"clip negative", "GET", "/api/v1/clip?seconds=-3", "", 422},
		{"clip over capacity", "GET", "/api/v1/clip?seconds=500", "", 422},
		{"clip not a number", "GET", "/api/v1/clip?seconds=ten", "", 422},
		{"clip bad format", "GET", "/api/v1/clip?seconds=10&format=avi", "", 422},
		{"config get", "GET", "/api/v1/config", "", 200},
		{"config patch ok", "PATCH", "/api/v1/config", `{"mirror_flip": false}`, 200},
		{"config bad profile", "PATCH", "/api/v1/config", `{"profile": "4k120"}`, 422},
		{"config bad json", "PATCH", "/api/v1/config", `{`, 422},
		{"healthz", "GET", "/healthz", "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(d *httpapi.Deps) {
				if strings.HasPrefix(tc.path, "/api/v1/clip") {
					// serve a real temp file for the 200 cases
					p := filepath.Join(t.TempDir(), "clip.mp4")
					os.WriteFile(p, []byte("fake-mp4"), 0o644)
					d.Clip = &fakeClip{path: p, dur: 10 * time.Second}
				}
			})
			resp := do(t, srv, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d (body: %s)", resp.StatusCode, tc.want, b)
			}
			if tc.want == 422 {
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
					t.Errorf("content-type = %q, want application/problem+json", ct)
				}
				var pb map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&pb); err != nil {
					t.Fatalf("problem body: %v", err)
				}
				if pb["title"] == nil || pb["status"] == nil {
					t.Errorf("problem+json missing title/status: %v", pb)
				}
			}
		})
	}
}

// Delay errors must carry the limits in the body (FR-11).
func TestDelayErrorCarriesLimits(t *testing.T) {
	srv := newServer(t, nil)
	resp := do(t, srv, "PUT", "/api/v1/delay", `{"seconds": 999}`)
	if resp.StatusCode != 422 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var pb struct {
		Min *float64 `json:"min_seconds"`
		Max *float64 `json:"max_seconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pb); err != nil {
		t.Fatal(err)
	}
	if pb.Min == nil || pb.Max == nil || *pb.Min != 0 || *pb.Max != 120 {
		t.Errorf("limits = %v/%v, want 0/120", pb.Min, pb.Max)
	}
}

// PUT /delay must take effect on the engine (delay readable back).
func TestDelayApplies(t *testing.T) {
	e := engine.New(ringbuf.New(time.Minute, 1<<20))
	srv := newServer(t, func(d *httpapi.Deps) { d.Delay = e })
	if resp := do(t, srv, "PUT", "/api/v1/delay", `{"seconds": 4.5}`); resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := e.Delay(); got != 4500*time.Millisecond {
		t.Errorf("engine delay = %v, want 4.5s", got)
	}
}

// ---- clip success + error mapping (FR-5, REQUIREMENTS §3) -------------------

func TestClipSuccessHeaders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clip.mp4")
	content := "fake-mp4-bytes"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClip{path: p, dur: 9983 * time.Millisecond}
	srv := newServer(t, func(d *httpapi.Deps) { d.Clip = fc })

	resp := do(t, srv, "GET", "/api/v1/clip?seconds=10&format=mjpeg", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("content-type = %q, want video/mp4", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("content-disposition = %q", cd)
	}
	if xd := resp.Header.Get("X-Clip-Duration"); xd != "9.983" {
		t.Errorf("X-Clip-Duration = %q, want 9.983", xd)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != content {
		t.Errorf("body = %q", b)
	}
	if fc.got.format != "mjpeg" || fc.got.n != 10*time.Second {
		t.Errorf("exporter called with (%v, %q)", fc.got.n, fc.got.format)
	}
	if !fc.cleaned {
		t.Error("clip temp file not cleaned up after serving")
	}
}

func TestClipBusyAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"slots busy", export.ErrBusy},
		{"empty buffer", window.ErrNoFrames},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, func(d *httpapi.Deps) { d.Clip = &fakeClip{err: tc.err} })
			resp := do(t, srv, "GET", "/api/v1/clip?seconds=10", "")
			if resp.StatusCode != 503 {
				t.Fatalf("status = %d, want 503", resp.StatusCode)
			}
			if ra := resp.Header.Get("Retry-After"); ra == "" {
				t.Error("missing Retry-After")
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
				t.Errorf("content-type = %q", ct)
			}
		})
	}
}

// ---- config roundtrip --------------------------------------------------------

func TestConfigPatchRoundtrip(t *testing.T) {
	srv := newServer(t, nil)
	resp := do(t, srv, "PATCH", "/api/v1/config", `{"mirror_flip": false, "focus_absolute": 42}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var r config.Runtime
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if r.MirrorFlip || r.FocusAbsolute != 42 {
		t.Errorf("patched runtime = %+v", r)
	}

	resp = do(t, srv, "GET", "/api/v1/config", "")
	var r2 config.Runtime
	if err := json.NewDecoder(resp.Body).Decode(&r2); err != nil {
		t.Fatal(err)
	}
	if r2 != r {
		t.Errorf("GET after PATCH = %+v, want %+v", r2, r)
	}
}

// ---- preview ------------------------------------------------------------------

type fakeFrames struct{ f frame.Frame }

func (f *fakeFrames) Newest() (frame.Frame, error) { return f.f, nil }

func TestPreviewStreamsMultipart(t *testing.T) {
	tick := make(chan time.Time)
	jpg := []byte{0xff, 0xd8, 0xff, 0xd9}
	srv := newServer(t, func(d *httpapi.Deps) {
		d.Frames = &fakeFrames{f: frame.Frame{Seq: 7, JPEG: jpg}}
		d.Ticker = func(time.Duration) (<-chan time.Time, func()) { return tick, func() {} }
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/preview", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace") {
		t.Fatalf("content-type = %q", ct)
	}
	tick <- time.Time{} // one frame
	buf := make([]byte, 256)
	n, err := io.ReadAtLeast(resp.Body, buf, 30)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	part := string(buf[:n])
	if !strings.Contains(part, "image/jpeg") {
		t.Errorf("part lacks image/jpeg header: %q", part)
	}
	if !strings.Contains(part, string(jpg)) {
		t.Errorf("part lacks JPEG payload")
	}
}
