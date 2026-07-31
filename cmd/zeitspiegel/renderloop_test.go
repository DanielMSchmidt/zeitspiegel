package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

var rlT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// stepClock is the injected wall clock for renderLoop tests; stepDisplay
// advances it on Render to simulate decode+present cost.
type stepClock struct{ t time.Time }

func (c *stepClock) now() time.Time { return c.t }

type stepDisplay struct {
	clk       *stepClock
	renderDur time.Duration
	rendered  []frame.Frame
}

func (d *stepDisplay) Render(f frame.Frame) error {
	d.clk.t = d.clk.t.Add(d.renderDur)
	d.rendered = append(d.rendered, f)
	return nil
}

func newTestLoop(buf *ringbuf.Buffer, delay time.Duration, renderDur time.Duration) (*renderLoop, *stepDisplay, *loopMetrics) {
	eng := engine.New(buf)
	eng.SetDelay(delay)
	clk := &stepClock{t: rlT0}
	disp := &stepDisplay{clk: clk, renderDur: renderDur}
	m := &loopMetrics{}
	return &renderLoop{
		eng:     eng,
		display: disp,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: m,
		now:     clk.now,
		budget:  time.Second / displayTickFPS,
	}, disp, m
}

func rlBuf(n int, interval time.Duration) *ringbuf.Buffer {
	b := ringbuf.New(time.Hour, 1<<30)
	for i := 0; i < n; i++ {
		b.Push(frame.Frame{Seq: uint64(i), CaptureTS: rlT0.Add(time.Duration(i) * interval), JPEG: []byte{0xff}})
	}
	return b
}

// UT-13: the tick's fire time — not the wall clock — drives frame selection.
func TestStepUsesTickTimeForSelection(t *testing.T) {
	l, disp, _ := newTestLoop(rlBuf(51, 100*time.Millisecond), time.Second, 0)
	// Wall clock far ahead of the tick timeline: selection must ignore it.
	l.now = func() time.Time { return rlT0.Add(24 * time.Hour) }
	if quit := l.step(rlT0.Add(2 * time.Second)); quit {
		t.Fatal("step returned quit")
	}
	if len(disp.rendered) != 1 || disp.rendered[0].Seq != 10 {
		t.Fatalf("rendered %v, want exactly seq 10 (target = tick 2s − delay 1s)", disp.rendered)
	}
}

// UT-13: a tick-to-tick delta beyond 1.5× budget counts as an overrun —
// the only visible trace of ticks time.Ticker silently dropped.
func TestStepCountsTickOverruns(t *testing.T) {
	l, _, m := newTestLoop(rlBuf(600, 16*time.Millisecond), 100*time.Millisecond, 0)
	budget := time.Second / displayTickFPS
	tk := rlT0.Add(time.Second)
	l.step(tk)
	tk = tk.Add(budget) // on time
	l.step(tk)
	if got := m.tickOverruns.Load(); got != 0 {
		t.Fatalf("on-time ticks: overruns = %d, want 0", got)
	}
	tk = tk.Add(3 * budget) // two ticks lost
	l.step(tk)
	if got := m.tickOverruns.Load(); got != 1 {
		t.Fatalf("late tick: overruns = %d, want 1", got)
	}
}

// UT-13: renders costing more than the tick budget are counted and the
// duration high-water mark is kept.
func TestStepCountsOverBudgetRenders(t *testing.T) {
	slow := 30 * time.Millisecond
	l, _, m := newTestLoop(rlBuf(51, 100*time.Millisecond), time.Second, slow)
	l.step(rlT0.Add(2 * time.Second))
	if got := m.renderOverBudget.Load(); got != 1 {
		t.Fatalf("renderOverBudget = %d, want 1", got)
	}
	if got := m.renderMaxUS.Load(); got != slow.Microseconds() {
		t.Fatalf("renderMaxUS = %d, want %d", got, slow.Microseconds())
	}
	if got := m.presented.Load(); got != 1 {
		t.Fatalf("presented = %d, want 1", got)
	}
}

// UT-13: miss kinds from the engine are counted per kind.
func TestStepCountsMisses(t *testing.T) {
	l, _, m := newTestLoop(ringbuf.New(time.Hour, 1<<30), 0, 0)
	l.step(rlT0)
	if got := m.missEmpty.Load(); got != 1 {
		t.Fatalf("missEmpty = %d, want 1", got)
	}

	l2, _, m2 := newTestLoop(rlBuf(11, 100*time.Millisecond), time.Hour, 0)
	l2.step(rlT0.Add(time.Second))
	if got := m2.missTooEarly.Load(); got != 1 {
		t.Fatalf("missTooEarly = %d, want 1", got)
	}
}

// UT-13: an unchanged selection is a held frame; consecutive holds form a
// streak whose maximum is the direct judder signal (a held streak ≥ 3 at
// 30 fps capture under the 60 Hz tick is a visible stutter).
func TestStepCountsHeldStreaks(t *testing.T) {
	buf := rlBuf(11, 100*time.Millisecond) // 0..1 s
	l, disp, m := newTestLoop(buf, 100*time.Millisecond, 0)
	tk := rlT0.Add(500 * time.Millisecond)
	l.step(tk) // renders seq 4
	for i := 0; i < 3; i++ {
		tk = tk.Add(time.Millisecond) // same frame still selected
		l.step(tk)
	}
	if got := m.held.Load(); got != 3 {
		t.Fatalf("held = %d, want 3", got)
	}
	if got := m.heldStreakMax.Load(); got != 3 {
		t.Fatalf("heldStreakMax = %d, want 3", got)
	}
	tk = tk.Add(100 * time.Millisecond) // next frame due
	l.step(tk)
	if len(disp.rendered) != 2 {
		t.Fatalf("rendered %d frames, want 2", len(disp.rendered))
	}
	if got := m.heldStreak.Load(); got != 0 {
		t.Fatalf("heldStreak after render = %d, want 0 (reset)", got)
	}
	if got := m.heldStreakMax.Load(); got != 3 {
		t.Fatalf("heldStreakMax after render = %d, want 3 (kept)", got)
	}
}
