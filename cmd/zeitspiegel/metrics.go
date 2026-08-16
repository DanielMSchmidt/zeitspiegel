package main

import "sync/atomic"

// loopMetrics counts what the render loop actually does, so a stutter seen
// on the TV maps to a cause in /debug/vars (NFR-3 observability, ST-4).
// Plain atomics — the loop writes, expvar snapshots; no locks, no expvar
// dependency in the hot path.
type loopMetrics struct {
	ticks            atomic.Uint64 // loop iterations
	presented        atomic.Uint64 // frames rendered to the display
	held             atomic.Uint64 // ticks where the selected frame was unchanged
	repaints         atomic.Uint64 // held ticks re-presented because the overlay changed
	tickOverruns     atomic.Uint64 // tick-to-tick delta > 1.5× budget (dropped ticker ticks)
	renderOverBudget atomic.Uint64 // renders slower than one tick interval
	missTooEarly     atomic.Uint64 // engine.MissTooEarly selections
	missEmpty        atomic.Uint64 // engine.MissEmpty selections
	selectErrors     atomic.Uint64 // unexpected buffer errors from selection
	renderLastUS     atomic.Int64  // last render duration, µs
	renderMaxUS      atomic.Int64  // slowest render since start, µs
	heldStreak       atomic.Uint64 // consecutive held ticks, reset on present
	heldStreakMax    atomic.Uint64 // longest streak — the direct judder signal
}

// snapshot renders the counters for expvar.Func. At 30 fps capture under
// the 60 Hz tick held ≈ presented is normal; stutter shows as
// held_streak_max ≥ 3, render_over_budget or tick_overruns climbing, or
// miss_* nonzero after warm-up.
func (m *loopMetrics) snapshot() any {
	return map[string]any{
		"ticks":              m.ticks.Load(),
		"presented":          m.presented.Load(),
		"held":               m.held.Load(),
		"repaints":           m.repaints.Load(),
		"tick_overruns":      m.tickOverruns.Load(),
		"render_over_budget": m.renderOverBudget.Load(),
		"miss_too_early":     m.missTooEarly.Load(),
		"miss_empty":         m.missEmpty.Load(),
		"select_errors":      m.selectErrors.Load(),
		"render_last_us":     m.renderLastUS.Load(),
		"render_max_us":      m.renderMaxUS.Load(),
		"held_streak":        m.heldStreak.Load(),
		"held_streak_max":    m.heldStreakMax.Load(),
	}
}

// maxInt64 raises v to n if n is larger (single-writer, but CAS keeps it
// correct if that ever changes).
func maxInt64(v *atomic.Int64, n int64) {
	for {
		cur := v.Load()
		if n <= cur || v.CompareAndSwap(cur, n) {
			return
		}
	}
}

// maxUint64 raises v to n if n is larger.
func maxUint64(v *atomic.Uint64, n uint64) {
	for {
		cur := v.Load()
		if n <= cur || v.CompareAndSwap(cur, n) {
			return
		}
	}
}
