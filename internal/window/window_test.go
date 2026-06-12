package window_test

import (
	"errors"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/window"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// bufAt returns a buffer with n frames at the given fps, seq = index.
func bufAt(n int, fps int) *ringbuf.Buffer {
	b := ringbuf.New(time.Hour, 1<<30)
	for i := 0; i < n; i++ {
		b.Push(frame.Frame{Seq: uint64(i), CaptureTS: t0.Add(time.Duration(i) * time.Second / time.Duration(fps))})
	}
	return b
}

// UT-8: [t−n, t] ⇒ count = n·fps ± 1.
func TestCutCount(t *testing.T) {
	b := bufAt(201, 10) // 0..20 s @10 fps
	w, err := window.Cut(b, t0.Add(20*time.Second), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(w.Frames); n < 50 || n > 52 { // 5 s · 10 fps ± 1 (inclusive bounds)
		t.Errorf("frame count = %d, want 51 ± 1", n)
	}
	first, last := w.Frames[0], w.Frames[len(w.Frames)-1]
	if first.CaptureTS.Before(t0.Add(15*time.Second - time.Millisecond)) {
		t.Errorf("first frame at %v, want ≥ t0+15s", first.CaptureTS)
	}
	if !last.CaptureTS.Equal(t0.Add(20 * time.Second)) {
		t.Errorf("last frame at %v, want t0+20s", last.CaptureTS)
	}
	if d := w.Duration - 5*time.Second; d < -200*time.Millisecond || d > 200*time.Millisecond {
		t.Errorf("duration = %v, want ≈ 5s", w.Duration)
	}
	for i := 1; i < len(w.Frames); i++ {
		if !w.Frames[i].CaptureTS.After(w.Frames[i-1].CaptureTS) {
			t.Fatalf("frames not in capture order at %d", i)
		}
	}
}

// UT-8: clamp when under-buffered (FR-5: duration = min(n, buffered)).
func TestCutClamps(t *testing.T) {
	b := bufAt(31, 10) // only 3 s buffered
	w, err := window.Cut(b, t0.Add(3*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(w.Frames); n != 31 {
		t.Errorf("frame count = %d, want all 31", n)
	}
	if d := w.Duration - 3*time.Second; d < -200*time.Millisecond || d > 200*time.Millisecond {
		t.Errorf("duration = %v, want ≈ 3 s (clamped)", w.Duration)
	}
}

// UT-8: empty buffer ⇒ error.
func TestCutEmpty(t *testing.T) {
	b := ringbuf.New(time.Minute, 1<<20)
	if _, err := window.Cut(b, t0, 5*time.Second); !errors.Is(err, window.ErrNoFrames) {
		t.Errorf("err = %v, want ErrNoFrames", err)
	}
}
