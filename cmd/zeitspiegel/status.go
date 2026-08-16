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
		// The same snapshot the capacity and the profile above came from, so
		// the body describes one moment rather than two reads of the config.
		Config: rt,
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
	mu      sync.Mutex
	rt      config.Runtime
	buf     *ringbuf.Buffer
	restart *atomic.Bool
	// setMirror is nil until a display exists — headless, or a unit still
	// waiting for its HDMI cable (FR-17). Set through attachMirror.
	setMirror func(bool)

	// overrides is everything ever set on this unit through the API, and save
	// writes it down so the next boot starts from it (FR-18). save is nil when
	// nothing is configured to persist to — a development run on a laptop. It
	// is called under the lock, which is what keeps the file's last write and
	// the store's current value the same decision; the writes are rare (only
	// an actual change) and go to a card, so a failure is the closure's to log
	// rather than the guest's to see: the setting has already taken effect.
	overrides config.Patch
	save      func(config.Patch)
}

// attachMirror wires a newly opened display's mirror toggle in, and hands it
// the flip that is in force rather than the one the config file booted with.
// The control page answers from boot now, so a guest can turn the mirroring
// off on a unit that has no screen yet; the screen must come up agreeing with
// what /api/v1/config already reports.
func (s *runtimeStore) attachMirror(setMirror func(bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setMirror = setMirror
	if setMirror != nil {
		setMirror(s.rt.MirrorFlip)
	}
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
	if rt == old {
		return rt, nil // nothing moved: no side effects, and nothing to write
	}
	s.rt = rt

	// Written down before the side effects, because this is the value the unit
	// is now running under and the next boot has to agree with it.
	if s.save != nil {
		s.overrides = s.overrides.Merge(p)
		s.save(s.overrides)
	}

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
