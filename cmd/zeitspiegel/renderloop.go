package main

import (
	"log/slog"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/engine"
)

// renderLoop is one display tick's worth of work, extracted from run() so
// the timing behavior is unit-testable (UT-13) with an injected clock and a
// fake display. run() owns the ticker and calls step with each tick's fire
// time; selection uses that timestamp, not the wall clock, so a tick that
// starts late does not additionally skew which frame is chosen.
type renderLoop struct {
	eng     *engine.Engine
	display engine.Display
	logger  *slog.Logger
	metrics *loopMetrics
	now     func() time.Time // wall clock for durations; injected in tests
	budget  time.Duration    // one tick interval
	start   time.Time        // process start, for the first-frame log

	// optional display capabilities (nil on headless builds)
	pump      func() bool
	setDelay  func(time.Duration)
	setWarmup func(time.Duration)
	splash    func() error
	// repaint re-presents the frame already on screen with the current
	// overlay, without decoding anything. Held ticks use it; see overlay.
	repaint func() error

	prevTick       time.Time
	firstFrameDone bool
	lastSelErr     string
	lastSlowLog    time.Time
	lastOverlay    overlay
}

// overlay is everything drawn on top of the frame: the delay badge and the
// warm-up countdown, both in whole seconds because that is the resolution
// they are shown at.
//
// It exists because the render loop's skip rule is about the *frame* — an
// unchanged selection is not redrawn (that is what keeps the tick budget) —
// while the badge and the countdown change on their own schedule. Warm-up is
// the case where the two diverge for a long stretch: the selected frame is
// pinned to the oldest one for the whole warm-up window, so without this the
// badge froze with the picture and a mirror counting down was
// indistinguishable from a crashed one.
type overlay struct {
	delayS  int
	warmupS int
}

// warmupRemaining is how much longer the buffer has to fill before it reaches
// back as far as the delay asks (FR-10). Zero once it does, and zero when
// there is no frame to measure from — an empty buffer is the splash's stretch,
// not a countdown.
//
// Derived from the tick's own fire time rather than the wall clock, for the
// same reason frame selection is: a late tick must not also skew the number
// on screen.
func warmupRemaining(sel engine.Selection, tickT time.Time, delay time.Duration) time.Duration {
	if sel.Miss != engine.MissTooEarly || sel.Frame.CaptureTS.IsZero() {
		return 0
	}
	// sel.Frame is the oldest frame buffered; the target sits before it.
	if d := sel.Frame.CaptureTS.Sub(tickT.Add(-delay)); d > 0 {
		return d
	}
	return 0
}

// step runs one tick; it reports true when the user closed the window.
func (l *renderLoop) step(tickT time.Time) (quit bool) {
	m := l.metrics
	m.ticks.Add(1)
	// time.Ticker's channel holds one tick: when a slow render overruns,
	// missed ticks vanish silently. The fire-time delta is their only trace.
	if !l.prevTick.IsZero() && tickT.Sub(l.prevTick) > l.budget*3/2 {
		m.tickOverruns.Add(1)
	}
	l.prevTick = tickT

	if l.pump != nil && l.pump() { // window closed (dev mode)
		return true
	}
	delay := l.eng.Delay()
	if l.setDelay != nil {
		l.setDelay(delay)
	}

	sel := l.eng.Tick(tickT)
	warm := warmupRemaining(sel, tickT, delay)
	if l.setWarmup != nil {
		l.setWarmup(warm)
	}
	// Whole seconds, matching what the badge and the countdown actually
	// print: a repaint is only worth a tick when the pixels would differ.
	// The countdown rounds up the same way screen.formatWarmup does.
	curOverlay := overlay{
		delayS:  int(delay / time.Second),
		warmupS: int((warm + time.Second - 1) / time.Second),
	}
	switch sel.Miss {
	case engine.MissTooEarly:
		m.missTooEarly.Add(1)
	case engine.MissEmpty:
		m.missEmpty.Add(1)
	}
	if sel.Err != nil {
		m.selectErrors.Add(1)
		if sel.Err.Error() != l.lastSelErr {
			l.lastSelErr = sel.Err.Error() // log once per distinct error, not per tick
			l.logger.Error("frame selection", "err", sel.Err)
		}
	} else {
		l.lastSelErr = ""
	}

	switch {
	case sel.Render:
		t0 := l.now()
		err := l.display.Render(sel.Frame)
		took := l.now().Sub(t0)
		m.renderLastUS.Store(took.Microseconds())
		maxInt64(&m.renderMaxUS, took.Microseconds())
		if took > l.budget {
			m.renderOverBudget.Add(1)
			// 2× budget guarantees a dropped tick — worth a journal line,
			// rate-limited so a slow stretch cannot flood the log.
			if took > 2*l.budget && t0.Sub(l.lastSlowLog) >= time.Second {
				l.lastSlowLog = t0
				l.logger.Warn("slow render", "took", took.Round(time.Millisecond), "seq", sel.Frame.Seq, "bytes", sel.Frame.Bytes())
			}
		}
		if err != nil {
			l.logger.Error("render", "seq", sel.Frame.Seq, "err", err)
			break
		}
		m.presented.Add(1)
		m.heldStreak.Store(0)
		l.lastOverlay = curOverlay
		if !l.firstFrameDone {
			l.firstFrameDone = true
			l.logger.Info("first frame presented",
				"since_start", l.now().Sub(l.start).Round(time.Millisecond),
				"uptime", procUptime())
		}
	default:
		if !sel.Frame.CaptureTS.IsZero() {
			// A frame is selected but unchanged since last tick: held.
			// Expected when capture is slower than the tick; a long streak
			// is judder.
			m.held.Add(1)
			maxUint64(&m.heldStreakMax, m.heldStreak.Add(1))
			// The frame is the same, but the badge or the countdown over it
			// may not be. Repaint costs a texture copy and a present — no
			// decode — and only when the text would actually differ.
			if curOverlay != l.lastOverlay && l.repaint != nil {
				if err := l.repaint(); err != nil {
					l.logger.Error("repaint", "err", err)
					break
				}
				m.repaints.Add(1)
				l.lastOverlay = curOverlay
			}
		}
		if !l.firstFrameDone && l.splash != nil {
			// Camera is still enumerating / buffer is empty. Repaint the
			// splash each tick so the screen shows our colour instead of
			// SDL's default.
			if err := l.splash(); err != nil {
				l.logger.Error("splash", "err", err)
			}
		}
	}
	return false
}
