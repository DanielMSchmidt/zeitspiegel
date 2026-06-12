package engine_test

import (
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// bufEvery fills a buffer with n frames, one every interval, seq = index.
func bufEvery(n int, interval time.Duration) *ringbuf.Buffer {
	b := ringbuf.New(time.Hour, 1<<30)
	for i := 0; i < n; i++ {
		b.Push(frame.Frame{Seq: uint64(i), CaptureTS: t0.Add(time.Duration(i) * interval)})
	}
	return b
}

// UT-6: target time with FakeClock; delay change effective next tick.
func TestTargetTimeAndDelayChange(t *testing.T) {
	e := engine.New(bufEvery(51, 100*time.Millisecond)) // 0..5 s
	e.SetDelay(2 * time.Second)
	clk := synth.NewFakeClock(t0.Add(3 * time.Second))

	sel := e.Tick(clk.Now()) // target 1.0 s ⇒ seq 10
	if !sel.Render || sel.WarmingUp || sel.Frame.Seq != 10 {
		t.Fatalf("tick@3s delay 2s: got (render=%v warming=%v seq=%d), want (true false 10)", sel.Render, sel.WarmingUp, sel.Frame.Seq)
	}

	e.SetDelay(1 * time.Second) // effective on the NEXT tick (FR-3)
	clk.Advance(100 * time.Millisecond)
	sel = e.Tick(clk.Now()) // target 3.1−1.0 = 2.1 s ⇒ seq 21
	if !sel.Render || sel.Frame.Seq != 21 {
		t.Fatalf("tick@3.1s delay 1s: got (render=%v seq=%d), want (true 21)", sel.Render, sel.Frame.Seq)
	}
	if got := e.Delay(); got != time.Second {
		t.Errorf("Delay() = %v, want 1s", got)
	}
}

// Renderer must skip when Seq hasn't changed between ticks (ARCHITECTURE §3).
func TestSkipUnchangedSeq(t *testing.T) {
	e := engine.New(bufEvery(51, 100*time.Millisecond))
	e.SetDelay(time.Second)
	now := t0.Add(2 * time.Second)
	if sel := e.Tick(now); !sel.Render {
		t.Fatal("first tick should render")
	}
	if sel := e.Tick(now.Add(time.Millisecond)); sel.Render {
		t.Fatal("second tick with same selected frame must not re-render")
	}
}

// UT-7 / FR-4: delay increase ⇒ the past replays exactly once; decrease ⇒
// forward jump with no double display.
func TestHardCutSemantics(t *testing.T) {
	e := engine.New(bufEvery(101, 100*time.Millisecond)) // 0..10 s
	clk := synth.NewFakeClock(t0.Add(2 * time.Second))
	e.SetDelay(time.Second)

	var rendered []uint64
	tick := func() {
		if sel := e.Tick(clk.Now()); sel.Render {
			rendered = append(rendered, sel.Frame.Seq)
		}
		clk.Advance(100 * time.Millisecond)
	}

	tick() // 2.0 → 10
	tick() // 2.1 → 11
	e.SetDelay(1500 * time.Millisecond)
	for i := 0; i < 8; i++ { // 2.2..2.9 → 7,8,...,14
		tick()
	}
	want := []uint64{10, 11, 7, 8, 9, 10, 11, 12, 13, 14}
	if len(rendered) != len(want) {
		t.Fatalf("rendered %v, want %v", rendered, want)
	}
	for i := range want {
		if rendered[i] != want[i] {
			t.Fatalf("rendered %v, want %v (replay must happen exactly once)", rendered, want)
		}
	}

	// decrease ⇒ jump forward, nothing displayed twice
	rendered = nil
	e.SetDelay(500 * time.Millisecond)
	for i := 0; i < 5; i++ { // 3.0..3.4 → 25,26,27,28,29
		tick()
	}
	prev := uint64(14) // last frame shown before the cut
	for i, s := range rendered {
		if s <= prev {
			t.Fatalf("frame %d: seq %d ≤ previous %d — double display after decrease (got %v)", i, s, prev, rendered)
		}
		prev = s
	}
	if rendered[0] != 25 {
		t.Errorf("first frame after decrease = %d, want 25 (hard jump)", rendered[0])
	}
}

// IT-6 / FR-10: delay 10 s but only 3 s buffered ⇒ oldest frame + warming_up.
func TestWarmUp(t *testing.T) {
	e := engine.New(bufEvery(31, 100*time.Millisecond)) // 0..3 s buffered
	e.SetDelay(10 * time.Second)
	sel := e.Tick(t0.Add(3 * time.Second)) // target −7 s, before oldest
	if !sel.WarmingUp {
		t.Error("want WarmingUp")
	}
	if !sel.Render || sel.Frame.Seq != 0 {
		t.Errorf("got (render=%v seq=%d), want oldest frame rendered (true, 0)", sel.Render, sel.Frame.Seq)
	}

	// empty buffer: nothing to render, still warming
	e2 := engine.New(ringbuf.New(time.Minute, 1<<20))
	sel2 := e2.Tick(t0)
	if sel2.Render || !sel2.WarmingUp {
		t.Errorf("empty buffer: got (render=%v warming=%v), want (false true)", sel2.Render, sel2.WarmingUp)
	}
}

// IT-1 / FR-1: @60 fps with delay 2.0 s every rendered frame satisfies
// capture_ts = render_ts − 2.0 s ± 17 ms.
func TestCoreDelayProperty(t *testing.T) {
	const fps = 60
	interval := time.Second / fps
	src := synth.NewSource(fps, t0)
	buf := ringbuf.New(time.Minute, 1<<30)
	e := engine.New(buf)
	e.SetDelay(2 * time.Second)
	clk := synth.NewFakeClock(t0)
	disp := synth.NewFakeDisplay(clk)

	for i := 0; i < 4*fps; i++ { // 4 s simulated: 2 s warm-up + 2 s measured
		buf.Push(src.Next())
		if sel := e.Tick(clk.Now()); sel.Render {
			if err := disp.Render(sel.Frame); err != nil {
				t.Fatal(err)
			}
		}
		clk.Advance(interval)
	}

	measured := 0
	for _, r := range disp.Records() {
		if r.RenderedAt.Before(t0.Add(2*time.Second + 17*time.Millisecond)) {
			continue // warm-up phase
		}
		seq, capTS, err := frame.ParseAPP4(r.Frame.JPEG)
		if err != nil {
			t.Fatalf("frame %d: %v", r.Frame.Seq, err)
		}
		if seq != r.Frame.Seq {
			t.Fatalf("APP4 seq %d != frame seq %d", seq, r.Frame.Seq)
		}
		lag := r.RenderedAt.Sub(capTS)
		if d := lag - 2*time.Second; d < -17*time.Millisecond || d > 17*time.Millisecond {
			t.Fatalf("frame %d: lag %v, want 2 s ± 17 ms", seq, lag)
		}
		measured++
	}
	if measured < fps { // sanity: the assertion actually ran on ~2 s of frames
		t.Fatalf("only %d frames measured, want ≥ %d", measured, fps)
	}
}
