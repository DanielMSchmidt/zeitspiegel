//go:build integration

package httpapi_test

// IT-5 / IT-8: real ffmpeg through the full HTTP stack (TESTPLAN tier 2).

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

const itFPS = 60

var itT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// realClock satisfies httpapi.Clock for tests that must run in real time.
type stampClock struct{ end time.Time }

func (c stampClock) Now() time.Time { return c.end }

func fullServer(t *testing.T, buf *ringbuf.Buffer, clk httpapi.Clock, slots int) *httptest.Server {
	t.Helper()
	e := engine.New(buf)
	deps := httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: &fakeStatus{st: defaultStatus()},
		Delay:  e,
		Clip: &httpapi.Clipper{
			Buffer:   buf,
			Exporter: export.New(t.TempDir(), slots),
			Clock:    clk,
			FPS:      itFPS,
		},
		Config: &fakeStore{r: config.Default().Runtime()},
	}
	srv := httptest.NewServer(httpapi.New(deps))
	t.Cleanup(srv.Close)
	return srv
}

func fill(b *ringbuf.Buffer, seconds int) (last time.Time) {
	src := synth.NewSource(itFPS, itT0)
	var f frame.Frame
	for i := 0; i < seconds*itFPS+1; i++ {
		f = src.Next()
		b.Push(f)
	}
	return f.CaptureTS
}

func fetchClip(t *testing.T, srv *httptest.Server, path string) (status int, file string, hdr http.Header) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, "", resp.Header
	}
	f, err := os.CreateTemp(t.TempDir(), "clip-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, f.Name(), resp.Header
}

func probeDuration(t *testing.T, file string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", filepath.Base(file), err)
	}
	d, err := strconv.ParseFloat(string(out[:len(out)-1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// IT-8: 3 parallel clips all valid, no interleaving; a 4th gets 503 +
// Retry-After while the slots are taken.
func TestParallelClipsAndSlotLimit(t *testing.T) {
	buf := ringbuf.New(time.Hour, 1<<31)
	last := fill(buf, 12)
	srv := fullServer(t, buf, stampClock{end: last}, 3)

	var wg sync.WaitGroup
	results := make([]struct {
		status int
		file   string
	}, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].status, results[i].file, _ = fetchClip(t, srv, "/api/v1/clip?seconds=8")
		}(i)
	}

	time.Sleep(300 * time.Millisecond) // all three must be mid-export now
	st, _, hdr := fetchClip(t, srv, "/api/v1/clip?seconds=8")
	if st != 503 {
		t.Errorf("4th concurrent clip: status = %d, want 503", st)
	} else if hdr.Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}

	wg.Wait()
	for i, r := range results {
		if r.status != 200 {
			t.Fatalf("clip %d: status = %d", i, r.status)
		}
		if d := probeDuration(t, r.file); d < 7.8 || d > 8.2 {
			t.Errorf("clip %d: duration = %v, want ≈ 8 s", i, d)
		}
	}
}

// IT-5 / FR-6: export never blocks capture — the capture-style loop keeps
// pushing at 60 fps with a 4-deep channel while a clip exports; the drop
// counter stays 0.
func TestExportDoesNotBlockCapture(t *testing.T) {
	buf := ringbuf.New(time.Hour, 1<<31)
	last := fill(buf, 12)
	srv := fullServer(t, buf, stampClock{end: last}, 3)

	var drops, pushed atomic.Int64
	stop := make(chan struct{})
	frames := make(chan frame.Frame, 4) // ARCHITECTURE §4: capacity 4, drop on overflow
	src := synth.NewSource(itFPS, last.Add(time.Second/itFPS))
	src.SetSeq(12*itFPS + 1)

	go func() { // ticker side: never blocks (FR-6)
		tick := time.NewTicker(time.Second / itFPS)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				close(frames)
				return
			case <-tick.C:
				select {
				case frames <- src.Next():
				default:
					drops.Add(1)
				}
			}
		}
	}()
	go func() { // writer side: single Push caller
		for f := range frames {
			buf.Push(f)
			pushed.Add(1)
		}
	}()

	status, file, _ := fetchClip(t, srv, "/api/v1/clip?seconds=10")
	close(stop)
	if status != 200 {
		t.Fatalf("clip status = %d", status)
	}
	if d := probeDuration(t, file); d < 9.8 || d > 10.2 {
		t.Errorf("clip duration = %v, want ≈ 10 s", d)
	}
	if n := drops.Load(); n != 0 {
		t.Errorf("dropped %d frames during export, want 0 (FR-6)", n)
	}
	if pushed.Load() == 0 {
		t.Error("capture loop pushed nothing — test did not exercise concurrency")
	}
}
