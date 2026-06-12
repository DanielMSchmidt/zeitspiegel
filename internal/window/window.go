// Package window computes the export window [end−n, end] over the ring
// buffer (pure, no logging, no wall clock — hard rule 6).
package window

import (
	"errors"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// ErrNoFrames is returned when the buffer holds nothing in the window.
var ErrNoFrames = errors.New("window: no frames in window")

// Buffer is the read side window needs from ringbuf.Buffer.
type Buffer interface {
	Range(from, to time.Time) []frame.Frame
	Oldest() (frame.Frame, error)
}

// Window is the frame set of one clip request.
type Window struct {
	Frames   []frame.Frame
	Duration time.Duration // estimated playback duration = span + one frame interval
}

// Cut returns the frames of [end−n, end], clamped to available history when
// under-buffered (FR-5). An empty result is an error.
func Cut(buf Buffer, end time.Time, n time.Duration) (Window, error) {
	from := end.Add(-n)
	if oldest, err := buf.Oldest(); err == nil && oldest.CaptureTS.After(from) {
		from = oldest.CaptureTS // clamp: under-buffered
	}
	frames := buf.Range(from, end)
	if len(frames) == 0 {
		return Window{}, ErrNoFrames
	}
	w := Window{Frames: frames}
	if len(frames) > 1 {
		span := frames[len(frames)-1].CaptureTS.Sub(frames[0].CaptureTS)
		w.Duration = span + span/time.Duration(len(frames)-1)
	}
	return w, nil
}
