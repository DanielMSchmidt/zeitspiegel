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

// FrameExporter encodes a frame window into a clip file (export.Exporter
// satisfies it; tests fake it).
type FrameExporter interface {
	Export(ctx context.Context, frames []frame.Frame, fps float64, format export.Format) (path string, cleanup func(), err error)
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
// pass through for the handler's 503 mapping.
func (c *Clipper) ExportClip(ctx context.Context, n time.Duration, format string) (Clip, error) {
	w, err := window.Cut(c.Buffer, c.Clock.Now(), n)
	if err != nil {
		return Clip{}, err
	}
	path, cleanup, err := c.Exporter.Export(ctx, w.Frames, c.FPS(), export.Format(format))
	if err != nil {
		return Clip{}, err
	}
	return Clip{Path: path, Duration: w.Duration, Cleanup: cleanup}, nil
}
