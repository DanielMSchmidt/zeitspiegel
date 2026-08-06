package httpapi

import (
	"context"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/window"
)

// Clock is injected wherever the API layer needs "now" (hard rule 6).
type Clock interface {
	Now() time.Time
}

// ClipBuffer is the buffer read side the clipper needs (ringbuf.Buffer
// satisfies it).
type ClipBuffer interface {
	Range(from, to time.Time) []frame.Frame
	Oldest() (frame.Frame, error)
}

// FrameExporter prepares a frame window for streaming (StreamExporter
// adapts export.Exporter; tests fake it).
type FrameExporter interface {
	Prepare(ctx context.Context, frames []frame.Frame, fps float64, format export.Format) (ClipStream, error)
}

// StreamExporter lifts *export.Exporter's concrete Prepare into
// FrameExporter (Go interfaces have no covariant returns).
type StreamExporter struct{ E *export.Exporter }

// Prepare implements FrameExporter.
func (s StreamExporter) Prepare(ctx context.Context, frames []frame.Frame, fps float64, format export.Format) (ClipStream, error) {
	st, err := s.E.Prepare(ctx, frames, fps, format)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Clipper is the production ClipExporter: cut the window ending now, pipe it
// through ffmpeg (FR-5).
type Clipper struct {
	Buffer   ClipBuffer
	Exporter FrameExporter
	Clock    Clock
	// FPS is consulted per export: the profile — and with it the nominal
	// frame rate — can change at runtime (PATCH /config, E-2).
	FPS func() float64
}

// ExportClip implements ClipExporter. window.ErrNoFrames and export.ErrBusy
// pass through for the handler's 503 mapping. The returned Clip holds an
// export slot; the caller must run Stream.WriteTo or Stream.Close.
func (c *Clipper) ExportClip(ctx context.Context, n time.Duration, format string) (Clip, error) {
	w, err := window.Cut(c.Buffer, c.Clock.Now(), n)
	if err != nil {
		return Clip{}, err
	}
	st, err := c.Exporter.Prepare(ctx, w.Frames, c.FPS(), export.Format(format))
	if err != nil {
		return Clip{}, err
	}
	return Clip{Duration: w.Duration, Stream: st}, nil
}
