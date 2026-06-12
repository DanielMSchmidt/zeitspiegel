// Package frame defines the immutable video frame passed between capture,
// buffer, display and export.
package frame

import "time"

// Frame is one captured MJPEG frame. JPEG is immutable after construction;
// the ring buffer hands out shared slices by design (ARCHITECTURE §D2/§5) —
// never mutate it and never copy it defensively.
type Frame struct {
	Seq       uint64
	CaptureTS time.Time
	JPEG      []byte
}

// Bytes returns the payload size used for buffer byte accounting.
func (f Frame) Bytes() int64 { return int64(len(f.JPEG)) }
