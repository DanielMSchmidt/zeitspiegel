package httpapi_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// recordExporter satisfies httpapi.FrameExporter and records the fps of each
// export without running ffmpeg.
type recordExporter struct{ fps []float64 }

type nopStream struct{}

func (nopStream) WriteTo(io.Writer) (int64, error) { return 0, nil }
func (nopStream) Close() error                     { return nil }

func (r *recordExporter) Prepare(_ context.Context, _ []frame.Frame, fps float64, _ export.Format) (httpapi.ClipStream, error) {
	r.fps = append(r.fps, fps)
	return nopStream{}, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// FR-5 / E-2: the clip frame rate follows the *runtime* profile. A profile
// PATCH between two exports must change the rate handed to ffmpeg, otherwise
// clips play at the wrong speed after a runtime profile change.
func TestClipperUsesCurrentFPS(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	buf := ringbuf.New(time.Minute, 1<<20)
	for i := 0; i < 10; i++ {
		buf.Push(frame.Frame{Seq: uint64(i), CaptureTS: start.Add(time.Duration(i) * 100 * time.Millisecond)})
	}

	rec := &recordExporter{}
	fps := 30.0
	c := &httpapi.Clipper{
		Buffer:   buf,
		Exporter: rec,
		Clock:    fixedClock{t: start.Add(time.Second)},
		FPS:      func() float64 { return fps },
	}

	if _, err := c.ExportClip(context.Background(), time.Second, "mp4"); err != nil {
		t.Fatal(err)
	}
	fps = 60 // the profile changed at runtime
	if _, err := c.ExportClip(context.Background(), time.Second, "mp4"); err != nil {
		t.Fatal(err)
	}

	if len(rec.fps) != 2 || rec.fps[0] != 30 || rec.fps[1] != 60 {
		t.Errorf("exporter saw fps %v, want [30 60] (fps must be read per export)", rec.fps)
	}
}
