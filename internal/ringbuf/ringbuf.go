// Package ringbuf provides the time-indexed in-RAM ring buffer of compressed
// frames at the core of the delay pipeline (ARCHITECTURE §D1).
package ringbuf

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

var (
	// ErrEmpty is returned when the buffer holds no frames.
	ErrEmpty = errors.New("ringbuf: empty")
	// ErrTooEarly is returned by At when t precedes the oldest buffered frame.
	ErrTooEarly = errors.New("ringbuf: requested time before oldest frame")
)

// Stats is a read-only snapshot of buffer occupancy.
type Stats struct {
	Len      int
	Bytes    int64
	Span     time.Duration // newest.CaptureTS − oldest.CaptureTS
	MaxDur   time.Duration
	MaxBytes int64
}

// Buffer is a slice-backed deque ordered by CaptureTS. One writer (the
// capture goroutine, hard rule 5), few readers; RWMutex contention is
// irrelevant at 60 Hz.
type Buffer struct {
	mu       sync.RWMutex
	frames   []frame.Frame
	bytes    int64
	maxDur   time.Duration
	maxBytes int64
}

// New creates a buffer that evicts oldest-first once the buffered span
// exceeds maxDur or the payload total exceeds maxBytes.
func New(maxDur time.Duration, maxBytes int64) *Buffer {
	return &Buffer{maxDur: maxDur, maxBytes: maxBytes}
}

// Push appends f and evicts from the front until both budgets hold. The
// just-pushed frame is never evicted. Only the capture goroutine may call
// Push.
func (b *Buffer) Push(f frame.Frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames = append(b.frames, f)
	b.bytes += f.Bytes()
	for len(b.frames) > 1 {
		span := b.frames[len(b.frames)-1].CaptureTS.Sub(b.frames[0].CaptureTS)
		if span <= b.maxDur && b.bytes <= b.maxBytes {
			break
		}
		b.bytes -= b.frames[0].Bytes()
		b.frames[0] = frame.Frame{} // release the slice for GC once exports drop it
		b.frames = b.frames[1:]
	}
}

// At returns the newest frame with CaptureTS ≤ t (binary search). ErrEmpty
// when the buffer is empty, ErrTooEarly when t precedes the oldest frame.
func (b *Buffer) At(t time.Time) (frame.Frame, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.frames) == 0 {
		return frame.Frame{}, ErrEmpty
	}
	// First index whose CaptureTS is after t; the frame before it is the match.
	i := sort.Search(len(b.frames), func(i int) bool {
		return b.frames[i].CaptureTS.After(t)
	})
	if i == 0 {
		return frame.Frame{}, ErrTooEarly
	}
	return b.frames[i-1], nil
}

// Range returns all frames with from ≤ CaptureTS ≤ to, in capture order.
// The returned slice is the caller's; the JPEG payloads are shared (this is
// how a running export pins its frames, ARCHITECTURE §3).
func (b *Buffer) Range(from, to time.Time) []frame.Frame {
	b.mu.RLock()
	defer b.mu.RUnlock()
	lo := sort.Search(len(b.frames), func(i int) bool {
		return !b.frames[i].CaptureTS.Before(from)
	})
	hi := sort.Search(len(b.frames), func(i int) bool {
		return b.frames[i].CaptureTS.After(to)
	})
	if lo >= hi {
		return nil
	}
	out := make([]frame.Frame, hi-lo)
	copy(out, b.frames[lo:hi])
	return out
}

// Oldest returns the oldest buffered frame.
func (b *Buffer) Oldest() (frame.Frame, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.frames) == 0 {
		return frame.Frame{}, ErrEmpty
	}
	return b.frames[0], nil
}

// Newest returns the most recently pushed frame.
func (b *Buffer) Newest() (frame.Frame, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.frames) == 0 {
		return frame.Frame{}, ErrEmpty
	}
	return b.frames[len(b.frames)-1], nil
}

// Stats returns an occupancy snapshot.
func (b *Buffer) Stats() Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	st := Stats{Len: len(b.frames), Bytes: b.bytes, MaxDur: b.maxDur, MaxBytes: b.maxBytes}
	if st.Len > 0 {
		st.Span = b.frames[st.Len-1].CaptureTS.Sub(b.frames[0].CaptureTS)
	}
	return st
}
