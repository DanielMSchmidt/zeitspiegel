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
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/peers"
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

	// Fleet identity (FR-15, E-8). A single appliance reports its own id and
	// name with role "primary" and fleet_size 1, so the shape is the same
	// whether one unit is running or three.
	UnitID    string `json:"unit_id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	FleetSize int    `json:"fleet_size"`
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

// ClipStream is a prepared clip body: WriteTo streams the encode as it is
// produced, Close releases the export slot if WriteTo never ran
// (export.Stream satisfies it).
type ClipStream interface {
	io.WriterTo
	io.Closer
}

// Clip is a prepared clip: duration known up front, export slot held,
// ffmpeg not started until Stream.WriteTo runs.
type Clip struct {
	Duration time.Duration
	Stream   ClipStream
}

// ClipExporter cuts and prepares the last n seconds for streaming (FR-5).
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

// PeerStore is the fleet membership list kept by whichever unit currently
// hosts the access point (peers.Registry satisfies it). Nil in Deps means
// this build serves no fleet API at all — a plain single appliance.
type PeerStore interface {
	Register(peers.Peer) error
	List() []peers.Peer
	Roster() []string
	Position(id string) int
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
	// Peers enables the fleet API (POST/GET /api/v1/peers). Nil ⇒ the
	// routes are not registered at all.
	Peers PeerStore
	// DelayedFrames serves the ?view=delayed preview: the frame at
	// now − delay (manual delay testing without the SDL display).
	DelayedFrames FrameProvider
	// Ticker paces the preview stream (injected so httpapi stays
	// wall-clock-free; cmd passes a real time.Ticker).
	Ticker  func(time.Duration) (<-chan time.Time, func())
	UI      http.Handler
	Healthy func() bool
	// Now supplies wall time for the clip stream's rolling write deadline
	// (injected — hard rule 6). nil disables the deadline (tests).
	Now func() time.Time
}

// PreviewInterval is the preview frame pacing (~10 fps, REQUIREMENTS §3).
const PreviewInterval = 100 * time.Millisecond

// maxBodyBytes bounds control-plane request bodies (PUT /delay, PATCH
// /config are tiny). Without it a client on the open AP (NFR-6) could
// stream an arbitrarily large JSON value into the decoder's memory.
const maxBodyBytes = 1 << 20

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
	if d.Peers != nil {
		mux.HandleFunc("POST /api/v1/peers", s.postPeer)
		mux.HandleFunc("GET /api/v1/peers", s.getPeers)
	}
	if d.UI != nil {
		mux.Handle("GET /", d.UI)
	}
	return withCORS(mux)
}

// withCORS lets the combined page, served by whichever unit is primary, talk
// straight to every other unit's API instead of being proxied through the
// primary — which keeps clip downloads off a second wireless hop.
//
// This widens nothing: the appliance is already unauthenticated on an
// isolated, internet-less LAN with no route to the internet in either
// direction (NFR-6). A JSON PUT is preflighted, so the OPTIONS answer is
// what actually makes a peer's delay slider work.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
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
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
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
	// Strict parse: Sscanf-style scanning would accept trailing garbage
	// ("5abc") and NaN — and NaN passes every range comparison, turning the
	// request into a full-buffer export (FR-11).
	sec, err := strconv.ParseFloat(q.Get("seconds"), 64)
	if err != nil || math.IsNaN(sec) || math.IsInf(sec, 0) || sec <= 0 || sec > capS {
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
	defer clip.Stream.Close()

	// Duration is known before the encode runs (window.Cut), so headers go
	// out first and the fragmented-MP4 body streams behind them. No
	// Content-Length ⇒ chunked transfer.
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="zeitspiegel-clip.mp4"`)
	w.Header().Set("X-Clip-Duration", fmt.Sprintf("%.3f", clip.Duration.Seconds()))

	n, err := clip.Stream.WriteTo(&clipWriter{w: w, rc: http.NewResponseController(w), now: s.d.Now})
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled) || r.Context().Err() != nil:
		s.d.Logger.Info("clip download aborted by client", "bytes", n)
	case n == 0:
		// Nothing written or flushed yet, so the 200 and video headers are
		// still unsent — replace them with a real error response.
		w.Header().Del("Content-Disposition")
		w.Header().Del("X-Clip-Duration")
		s.d.Logger.Error("clip export", "err", err)
		s.problem(w, http.StatusInternalServerError, "export failed", err.Error(), nil)
	default:
		// Mid-stream failure: headers are long gone. Abort the connection
		// without a terminating chunk so the client marks the download
		// failed instead of keeping a silently truncated file.
		s.d.Logger.Error("clip stream failed mid-transfer", "err", err, "bytes", n)
		panic(http.ErrAbortHandler)
	}
}

// clipWriteTimeout is the rolling per-write deadline for clip downloads: the
// server has no global WriteTimeout (preview streams are unbounded), but a
// stalled clip client must not pin the export slot and its frames forever.
const clipWriteTimeout = 30 * time.Second

// clipWriter forwards the export stream to the response, flushing each chunk
// (first fragment reaches the browser immediately) and refreshing the write
// deadline (stalled client ⇒ write error ⇒ ffmpeg killed upstream).
type clipWriter struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	now func() time.Time
}

func (c *clipWriter) Write(p []byte) (int, error) {
	if c.now != nil {
		// Best-effort: an unsupported deadline just leaves the old behavior.
		_ = c.rc.SetWriteDeadline(c.now().Add(clipWriteTimeout))
	}
	n, err := c.w.Write(p)
	if err == nil {
		_ = c.rc.Flush()
	}
	return n, err
}

func (s *server) getConfig(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, s.d.Config.Current())
}

func (s *server) patchConfig(w http.ResponseWriter, r *http.Request) {
	var p config.Patch
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
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

// postPeer records a member's heartbeat. The announcement deliberately
// carries no address: the member's address is taken from the connection it
// arrived on, so a member never has to discover its own DHCP lease and every
// card can ship the identical image (E-8).
func (s *server) postPeer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Port int    `json:"port"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.problem(w, http.StatusUnprocessableEntity, "invalid announcement",
			`body must be {"id": "...", "name": "...", "port": n}`, nil)
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		s.problem(w, http.StatusUnprocessableEntity, "invalid announcement",
			fmt.Sprintf("port %d outside 1…65535", body.Port), nil)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port on RemoteAddr (some test transports); use it whole.
		host = r.RemoteAddr
	}
	if host == "" {
		s.problem(w, http.StatusUnprocessableEntity, "invalid announcement",
			"could not determine the caller's address", nil)
		return
	}
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(body.Port))

	if err := s.d.Peers.Register(peers.Peer{ID: body.ID, Name: body.Name, BaseURL: base}); err != nil {
		if errors.Is(err, peers.ErrInvalid) {
			s.problem(w, http.StatusUnprocessableEntity, "invalid announcement", err.Error(), nil)
			return
		}
		s.d.Logger.Error("peer register", "err", err)
		s.problem(w, http.StatusInternalServerError, "register failed", err.Error(), nil)
		return
	}

	// The roster and position are what the member feeds back into its
	// election machine, so it knows how long to wait before promoting if
	// this unit disappears.
	s.writeJSON(w, peers.RegisterResponse{
		ID:       body.ID,
		BaseURL:  base,
		Roster:   s.d.Peers.Roster(),
		Position: s.d.Peers.Position(body.ID),
	})
}

func (s *server) getPeers(w http.ResponseWriter, _ *http.Request) {
	list := s.d.Peers.List()
	if list == nil {
		list = []peers.Peer{}
	}
	s.writeJSON(w, map[string]any{"peers": list})
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
	frames := s.d.Frames
	switch view := r.URL.Query().Get("view"); view {
	case "", "live":
	case "delayed":
		if s.d.DelayedFrames == nil {
			s.problem(w, http.StatusUnprocessableEntity, "delayed view unavailable",
				"this deployment serves only the live view", nil)
			return
		}
		frames = s.d.DelayedFrames
	default:
		s.problem(w, http.StatusUnprocessableEntity, "invalid preview view",
			fmt.Sprintf("view %q must be live or delayed", view), nil)
		return
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+previewBoundary)
	w.WriteHeader(http.StatusOK)
	flush, _ := w.(http.Flusher)
	if flush != nil {
		flush.Flush() // headers must reach the client before the first frame arrives
	}

	tick, stop := s.d.Ticker(PreviewInterval)
	defer stop()
	var lastSeq uint64
	var lastTS time.Time
	var sent bool
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick:
		}
		f, err := frames.Newest()
		// Frame identity is (Seq, CaptureTS): a source reconnect restarts
		// Seq at 0, so Seq alone could suppress a genuinely new frame.
		if err != nil || (sent && f.Seq == lastSeq && f.CaptureTS.Equal(lastTS)) {
			continue
		}
		lastSeq, lastTS, sent = f.Seq, f.CaptureTS, true
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
