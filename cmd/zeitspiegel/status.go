package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// minLatencyMS is the latency floor estimate (exposure+USB+decode+render+
// vsync, ARCHITECTURE §6); replaced by the measured glass-to-glass value
// from milestone M3.
const minLatencyMS = 80

// sysStatus composes GET /api/v1/status from the live components.
type sysStatus struct {
	start time.Time
	cfg   config.Config
	store *runtimeStore
	buf   *ringbuf.Buffer
	eng   *engine.Engine
	sup   *capture.Supervisor
	fleet *fleetRuntime
	mode  *modeStore // live capture mode; nil ⇒ profile nominal only
}

func (s *sysStatus) Status() httpapi.Status {
	st := s.buf.Stats()
	rt := s.store.Current()
	// Report what the camera actually opened, not what the profile nominally
	// asks for — on "auto" the probe may have traded resolution for frame
	// rate (E-2), and a status line that hides that is a lie.
	w, h := profileResolution(rt.Profile)
	fps := profileFPS(rt.Profile)
	if s.mode != nil {
		w, h = s.mode.resolution(rt.Profile)
		fps = s.mode.fps(rt.Profile)
	}
	filled := st.Span.Seconds()

	// Warm-up mirrors the engine's FR-10 semantics exactly: warming when
	// nothing is buffered or the delay target precedes the oldest frame.
	warming := true
	if oldest, err := s.buf.Oldest(); err == nil {
		warming = time.Now().Add(-s.eng.Delay()).Before(oldest.CaptureTS)
	}

	out := httpapi.Status{
		DelayS:        s.eng.Delay().Seconds(),
		FPS:           fps,
		Resolution:    fmt.Sprintf("%dx%d", w, h),
		Buffer:        httpapi.BufferStatus{CapacityS: rt.BufferMaxS, FilledS: filled, Bytes: st.Bytes},
		DroppedFrames: s.sup.Dropped(),
		MinLatencyMS:  minLatencyMS,
		WarmingUp:     warming,
		UptimeS:       time.Since(s.start).Seconds(),
	}
	// Identity is what labels this unit's card on the combined page, and
	// role is what marks which unit is currently hosting the network.
	if s.fleet != nil {
		out.UnitID = s.fleet.unit.ID
		out.Name = s.fleet.unit.Name
		out.Role = s.fleet.Role()
	}
	return out
}

func profileFPS(profile string) float64 {
	c := config.Default()
	c.Profile = profile
	return c.FPS()
}

func profileResolution(profile string) (int, int) {
	c := config.Default()
	c.Profile = profile
	return c.Resolution()
}

// runtimeStore owns the runtime config (single-writer semantics,
// REQUIREMENTS §3): HTTP PATCHes are serialized here; readers get snapshots.
type runtimeStore struct {
	mu        sync.Mutex
	rt        config.Runtime
	buf       *ringbuf.Buffer
	restart   *atomic.Bool
	setMirror func(bool) // nil when headless
}

func (s *runtimeStore) Current() config.Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rt
}

// Apply validates the patch and triggers side effects: mirror flips live,
// buffer budget resizes live, profile/camera-control changes clear the
// buffer and signal a pipeline restart (the supervisor reopens the source
// with the new settings).
func (s *runtimeStore) Apply(p config.Patch) (config.Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.rt
	rt, err := old.WithPatch(p)
	if err != nil {
		return config.Runtime{}, err
	}
	s.rt = rt

	if rt.MirrorFlip != old.MirrorFlip && s.setMirror != nil {
		s.setMirror(rt.MirrorFlip)
	}
	if rt.BufferMaxS != old.BufferMaxS {
		s.buf.SetMaxDuration(time.Duration(rt.BufferMaxS * float64(time.Second)))
	}
	if rt.Profile != old.Profile {
		s.buf.Clear() // profile change ⇒ hard cut, stale frames are the wrong size
		s.restart.Store(true)
	} else if rt.FocusAuto != old.FocusAuto || rt.FocusAbsolute != old.FocusAbsolute ||
		rt.ExposureAuto != old.ExposureAuto || rt.ExposureAbsolute != old.ExposureAbsolute {
		s.restart.Store(true) // camera controls are applied at open (FR-9)
	}
	return rt, nil
}
