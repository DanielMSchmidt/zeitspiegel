// Package httpapi exposes the versioned REST API and static UI
// (REQUIREMENTS §3). Handlers depend on small interfaces; errors are
// RFC-9457 application/problem+json. No wall clock here — anything timed is
// injected (hard rule 6).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/window"
)

// BufferStatus describes ring buffer occupancy in /status.
type BufferStatus struct {
	CapacityS float64 `json:"capacity_s"`
	FilledS   float64 `json:"filled_s"`
	Bytes     int64   `json:"bytes"`
}

// Status is the GET /api/v1/status response (FR-8).
type Status struct {
	DelayS        float64      `json:"delay_s"`
	FPS           float64      `json:"fps"`
	Resolution    string       `json:"resolution"`
	Buffer        BufferStatus `json:"buffer"`
	DroppedFrames uint64       `json:"dropped_frames"`
	MinLatencyMS  int64        `json:"min_latency_ms"`
	WarmingUp     bool         `json:"warming_up"`
	UptimeS       float64      `json:"uptime_s"`
}

// StatusProvider supplies the composed system status (wired in cmd).
type StatusProvider interface {
	Status() Status
}

// DelaySetter is implemented by engine.Engine (hard rule 5: the HTTP
// handler is the only delay writer).
type DelaySetter interface {
	Delay() time.Duration
	SetDelay(time.Duration)
}

// Clip is one exported clip ready to serve.
type Clip struct {
	Path     string
	Duration time.Duration
	Cleanup  func()
}

// ClipExporter cuts and encodes the last n seconds (FR-5).
type ClipExporter interface {
	ExportClip(ctx context.Context, n time.Duration, format string) (Clip, error)
}

// ConfigStore reads and patches the runtime configuration (FR-9).
type ConfigStore interface {
	Current() config.Runtime
	Apply(config.Patch) (config.Runtime, error)
}

// FrameProvider supplies the newest frame for the MJPEG preview.
type FrameProvider interface {
	Newest() (frame.Frame, error)
}

// Deps wires the handlers. Logger, Status, Delay, Clip and Config are
// required; Frames+Ticker enable /preview, UI serves /, Health overrides
// the /healthz check.
type Deps struct {
	Logger *slog.Logger
	Status StatusProvider
	Delay  DelaySetter
	Clip   ClipExporter
	Config ConfigStore
	Frames FrameProvider
	// Ticker paces the preview stream (injected so httpapi stays
	// wall-clock-free; cmd passes a real time.Ticker).
	Ticker  func(time.Duration) (<-chan time.Time, func())
	UI      http.Handler
	Healthy func() bool
}

// PreviewInterval is the preview frame pacing (~10 fps, REQUIREMENTS §3).
const PreviewInterval = 100 * time.Millisecond

// New assembles the ServeMux (stdlib patterns; no router framework).
func New(d Deps) http.Handler {
	s := &server{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", s.getStatus)
	mux.HandleFunc("PUT /api/v1/delay", s.putDelay)
	mux.HandleFunc("GET /api/v1/clip", s.getClip)
	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PATCH /api/v1/config", s.patchConfig)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /debug/vars", expvar.Handler())
	if d.Frames != nil && d.Ticker != nil {
		mux.HandleFunc("GET /api/v1/preview", s.getPreview)
	}
	if d.UI != nil {
		mux.Handle("GET /", d.UI)
	}
	return mux
}

type server struct {
	d Deps
}

// problem writes an RFC-9457 response; extra keys become extension members.
func (s *server) problem(w http.ResponseWriter, status int, title, detail string, extra map[string]any) {
	body := map[string]any{
		"type":   "about:blank",
		"title":  title,
		"status": status,
		"detail": detail,
	}
	for k, v := range extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.d.Logger.Error("write problem response", "err", err)
	}
}

func (s *server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.d.Logger.Error("write json response", "err", err)
	}
}

func (s *server) getStatus(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, s.d.Status.Status())
}

func (s *server) putDelay(w http.ResponseWriter, r *http.Request) {
	capS := s.d.Status.Status().Buffer.CapacityS
	limits := map[string]any{"min_seconds": 0.0, "max_seconds": capS}
	var body struct {
		Seconds *float64 `json:"seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Seconds == nil {
		s.problem(w, http.StatusUnprocessableEntity, "invalid delay request",
			`body must be {"seconds": <number>}`, limits)
		return
	}
	sec := *body.Seconds
	if sec < 0 || sec > capS {
		s.problem(w, http.StatusUnprocessableEntity, "delay out of range",
			fmt.Sprintf("seconds %g outside 0…%g", sec, capS), limits)
		return
	}
	s.d.Delay.SetDelay(time.Duration(sec * float64(time.Second)))
	s.writeJSON(w, map[string]float64{"seconds": sec})
}

func (s *server) getClip(w http.ResponseWriter, r *http.Request) {
	capS := s.d.Status.Status().Buffer.CapacityS
	q := r.URL.Query()
	var sec float64
	if _, err := fmt.Sscanf(q.Get("seconds"), "%g", &sec); err != nil || sec <= 0 || sec > capS {
		s.problem(w, http.StatusUnprocessableEntity, "invalid clip request",
			fmt.Sprintf("seconds %q must be a number in 0…%g", q.Get("seconds"), capS),
			map[string]any{"max_seconds": capS})
		return
	}
	format := q.Get("format")
	if format == "" {
		format = "mp4"
	}
	if format != "mp4" && format != "mjpeg" {
		s.problem(w, http.StatusUnprocessableEntity, "invalid clip format",
			fmt.Sprintf("format %q must be mp4 or mjpeg", format), nil)
		return
	}

	clip, err := s.d.Clip.ExportClip(r.Context(), time.Duration(sec*float64(time.Second)), format)
	switch {
	case errors.Is(err, export.ErrBusy):
		w.Header().Set("Retry-After", "2")
		s.problem(w, http.StatusServiceUnavailable, "export slots busy", err.Error(), nil)
		return
	case errors.Is(err, window.ErrNoFrames):
		w.Header().Set("Retry-After", "2")
		s.problem(w, http.StatusServiceUnavailable, "buffer empty", err.Error(), nil)
		return
	case err != nil:
		s.d.Logger.Error("clip export", "err", err)
		s.problem(w, http.StatusInternalServerError, "export failed", err.Error(), nil)
		return
	}
	defer clip.Cleanup()

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="zeitspiegel-clip.mp4"`)
	w.Header().Set("X-Clip-Duration", fmt.Sprintf("%.3f", clip.Duration.Seconds()))
	http.ServeFile(w, r, clip.Path)
}

func (s *server) getConfig(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, s.d.Config.Current())
}

func (s *server) patchConfig(w http.ResponseWriter, r *http.Request) {
	var p config.Patch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		s.problem(w, http.StatusUnprocessableEntity, "invalid config patch", err.Error(), nil)
		return
	}
	rt, err := s.d.Config.Apply(p)
	switch {
	case errors.Is(err, config.ErrInvalid):
		s.problem(w, http.StatusUnprocessableEntity, "invalid config value", err.Error(), nil)
		return
	case err != nil:
		s.d.Logger.Error("config apply", "err", err)
		s.problem(w, http.StatusInternalServerError, "config apply failed", err.Error(), nil)
		return
	}
	s.writeJSON(w, rt)
}

func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	if s.d.Healthy != nil && !s.d.Healthy() {
		s.problem(w, http.StatusServiceUnavailable, "unhealthy", "pipeline not running", nil)
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

const previewBoundary = "zeitspiegelframe"

func (s *server) getPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+previewBoundary)
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	if flush != nil {
		flush.Flush() // headers must reach the client before the first frame arrives
	}

	tick, stop := s.d.Ticker(PreviewInterval)
	defer stop()
	var lastSeq uint64
	var sent bool
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick:
		}
		f, err := s.d.Frames.Newest()
		if err != nil || (sent && f.Seq == lastSeq) {
			continue
		}
		lastSeq, sent = f.Seq, true
		if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
			previewBoundary, len(f.JPEG)); err != nil {
			return
		}
		if _, err := w.Write(f.JPEG); err != nil {
			return
		}
		fmt.Fprint(w, "\r\n")
		if flush != nil {
			flush.Flush()
		}
	}
}
