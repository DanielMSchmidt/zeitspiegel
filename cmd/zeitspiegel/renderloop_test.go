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

// warmupSpy records what the loop told the display about the warm-up, and
// how often it asked for a repaint of unchanged pixels.
type warmupSpy struct {
	warmups  []time.Duration
	repaints int
}

func (w *warmupSpy) setWarmup(d time.Duration) { w.warmups = append(w.warmups, d) }
func (w *warmupSpy) repaint() error            { w.repaints++; return nil }

func (w *warmupSpy) attach(l *renderLoop) {
	l.setWarmup = w.setWarmup
	l.repaint = w.repaint
}

func (w *warmupSpy) last() time.Duration {
	if len(w.warmups) == 0 {
		return -1
	}
	return w.warmups[len(w.warmups)-1]
}

// UT-44: how long the mirror still has to warm up is derived from the tick's
// own fire time and the oldest frame it can reach — the number the on-screen
// countdown shows (FR-10).
func TestStepReportsWarmupRemaining(t *testing.T) {
	// Buffer holds T0…T0+3s; at tick T0+5s a 10 s delay targets T0−5s, which
	// is 5 s before the oldest frame there is.
	l, _, _ := newTestLoop(rlBuf(31, 100*time.Millisecond), 10*time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	l.step(rlT0.Add(5 * time.Second))
	if got, want := spy.last(), 5*time.Second; got != want {
		t.Fatalf("warm-up remaining = %v, want %v", got, want)
	}
}

// UT-44: once the buffer reaches back far enough there is nothing to count
// down, and the line comes off the screen.
func TestStepReportsNoWarmupOnceBuffered(t *testing.T) {
	l, _, _ := newTestLoop(rlBuf(31, 100*time.Millisecond), time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	l.step(rlT0.Add(2 * time.Second)) // target T0+1s, well inside the buffer
	if got := spy.last(); got != 0 {
		t.Fatalf("warm-up remaining = %v, want 0 — the delay is fully buffered", got)
	}
}

// UT-44: an empty buffer has no oldest frame to count from, so there is no
// countdown to show — that stretch is the splash's job.
func TestStepReportsNoWarmupOnEmptyBuffer(t *testing.T) {
	l, _, _ := newTestLoop(ringbuf.New(time.Hour, 1<<30), 10*time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	l.step(rlT0)
	if got := spy.last(); got != 0 {
		t.Fatalf("warm-up remaining = %v on an empty buffer, want 0", got)
	}
}

// UT-44: the fix for the field report. During warm-up the selected frame
// never changes, so the loop skips Render — which used to mean the badge and
// the countdown froze with the picture, and a mirror counting down looked
// exactly like a crashed one. A held tick repaints when the text changed.
func TestStepRepaintsHeldFrameWhenCountdownMoves(t *testing.T) {
	l, disp, _ := newTestLoop(rlBuf(31, 100*time.Millisecond), 10*time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	l.step(rlT0.Add(5 * time.Second)) // renders the oldest frame, 5s to go
	if len(disp.rendered) != 1 {
		t.Fatalf("rendered %d frames, want 1", len(disp.rendered))
	}
	if spy.repaints != 0 {
		t.Fatalf("repaints = %d after a real render, want 0", spy.repaints)
	}

	// Half a second later: still the same oldest frame, and 4.5 s still reads
	// as "ready in 5s". Nothing on screen changed, so nothing is redrawn.
	l.step(rlT0.Add(5500 * time.Millisecond))
	if spy.repaints != 0 {
		t.Fatalf("repaints = %d, want 0 — the countdown still reads the same", spy.repaints)
	}

	// Past the second boundary: 3.9 s left reads as "ready in 4s".
	l.step(rlT0.Add(6100 * time.Millisecond))
	if spy.repaints != 1 {
		t.Fatalf("repaints = %d, want 1 — the countdown ticked over", spy.repaints)
	}
	if len(disp.rendered) != 1 {
		t.Fatalf("rendered %d frames, want 1 — a repaint must not decode again", len(disp.rendered))
	}
}

// UT-44: moving the slider while the picture is held must reach the badge.
// This is what "I changed the delay and nothing happened" was.
func TestStepRepaintsHeldFrameWhenDelayChanges(t *testing.T) {
	l, disp, _ := newTestLoop(rlBuf(31, 100*time.Millisecond), 10*time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	tk := rlT0.Add(5 * time.Second)
	l.step(tk)

	// Same tick timeline, new delay: still warming, still the oldest frame,
	// but the badge now has to say 20s.
	l.eng.SetDelay(20 * time.Second)
	l.step(tk.Add(time.Millisecond))
	if spy.repaints != 1 {
		t.Fatalf("repaints = %d after a delay change, want 1", spy.repaints)
	}
	if len(disp.rendered) != 1 {
		t.Fatalf("rendered %d frames, want 1", len(disp.rendered))
	}
}

// UT-44: a held frame with nothing new to say is still left alone — the
// skip rule is what keeps the render budget (NFR-3), and this must not
// quietly turn into a repaint on every tick.
func TestStepDoesNotRepaintAnUnchangedScreen(t *testing.T) {
	l, _, m := newTestLoop(rlBuf(31, 100*time.Millisecond), time.Second, 0)
	spy := &warmupSpy{}
	spy.attach(l)

	tk := rlT0.Add(2 * time.Second)
	l.step(tk)
	for i := 0; i < 5; i++ {
		tk = tk.Add(time.Millisecond)
		l.step(tk)
	}
	if got := m.held.Load(); got != 5 {
		t.Fatalf("held = %d, want 5", got)
	}
	if spy.repaints != 0 {
		t.Fatalf("repaints = %d, want 0 — nothing on screen changed", spy.repaints)
	}
}
