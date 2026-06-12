// Package engine holds the pure frame-selection logic of the delay pipeline:
// which frame belongs to tick t, hard-cut delay-change semantics (FR-4) and
// warm-up (FR-10). No wall-clock time, no logging (hard rules 5/6).
package engine

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// Clock abstracts time for everything outside cmd/ and the hardware
// adapters. Production wiring injects the real clock; tests use
// synth.FakeClock.
type Clock interface {
	Now() time.Time
}

// Display renders one frame. Implemented by screen (SDL) and synth.FakeDisplay.
type Display interface {
	Render(frame.Frame) error
}

// FrameBuffer is the read side of the ring buffer the engine selects from.
type FrameBuffer interface {
	At(time.Time) (frame.Frame, error)
	Oldest() (frame.Frame, error)
}

// Selection is the outcome of one tick.
type Selection struct {
	Frame     frame.Frame
	Render    bool // false when the selected Seq did not change or nothing is buffered
	WarmingUp bool // delay exceeds buffered history (FR-10)
}

// Engine selects the frame for each display tick. The delay is an atomic
// written only by the HTTP handler and read by the render loop each tick,
// which makes delay changes effective within one tick by construction
// (FR-3, hard rule 5).
type Engine struct {
	buf      FrameBuffer
	delayNS  atomic.Int64
	lastSeq  uint64
	rendered bool // false until the first frame was selected for render
}

// New returns an engine reading from buf with delay 0.
func New(buf FrameBuffer) *Engine {
	return &Engine{buf: buf}
}

// SetDelay stores the new delay; only the HTTP handler calls this. Range
// validation against buffer capacity is the API layer's job (FR-11).
func (e *Engine) SetDelay(d time.Duration) {
	e.delayNS.Store(int64(d))
}

// Delay returns the current delay.
func (e *Engine) Delay() time.Duration {
	return time.Duration(e.delayNS.Load())
}

// Tick selects the frame for display time now. Only the render goroutine
// calls Tick. Hard-cut semantics fall out of pure target-time selection:
// a delay increase moves the target back so the past replays exactly once;
// a decrease jumps forward and the Seq-skip rule prevents double display.
func (e *Engine) Tick(now time.Time) Selection {
	target := now.Add(-e.Delay())
	f, err := e.buf.At(target)
	var warming bool
	switch {
	case err == nil:
	case errors.Is(err, ringbuf.ErrTooEarly): // delay > buffered: show oldest
		warming = true
		if f, err = e.buf.Oldest(); err != nil {
			return Selection{WarmingUp: true}
		}
	case errors.Is(err, ringbuf.ErrEmpty):
		return Selection{WarmingUp: true}
	default:
		return Selection{WarmingUp: true}
	}
	if e.rendered && f.Seq == e.lastSeq {
		return Selection{Frame: f, Render: false, WarmingUp: warming}
	}
	e.lastSeq = f.Seq
	e.rendered = true
	return Selection{Frame: f, Render: true, WarmingUp: warming}
}
