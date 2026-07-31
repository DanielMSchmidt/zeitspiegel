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
	pump     func() bool
	setDelay func(time.Duration)
	splash   func() error

	prevTick       time.Time
	firstFrameDone bool
	lastSelErr     string
	lastSlowLog    time.Time
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
	if l.setDelay != nil {
		l.setDelay(l.eng.Delay())
	}

	sel := l.eng.Tick(tickT)
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
