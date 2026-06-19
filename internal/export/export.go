// Package export turns a frame window into a downloadable clip by piping
// JPEGs through an ffmpeg subprocess (ARCHITECTURE §D4). ffmpeg is always a
// subprocess, never a linked library (hard rule 7).
package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// ErrBusy is returned when all export slots are taken (maps to 503 +
// Retry-After, REQUIREMENTS §3).
var ErrBusy = errors.New("export: all export slots busy")

// Format selects the clip encoding.
type Format string

const (
	// FormatMP4 transcodes to H.264 (default — playable on phones, FR-5).
	FormatMP4 Format = "mp4"
	// FormatMJPEG stream-copies the buffered JPEGs into an MP4 container
	// (CPU-free).
	FormatMJPEG Format = "mjpeg"
)

// Exporter runs at most `slots` concurrent ffmpeg exports (3 in production,
// TESTPLAN IT-8).
type Exporter struct {
	dir    string
	ffmpeg string
	sem    chan struct{}
}

// New creates an exporter writing temp files to dir (tmpfs on the Pi).
func New(dir string, slots int) *Exporter {
	return &Exporter{dir: dir, ffmpeg: "ffmpeg", sem: make(chan struct{}, slots)}
}

// Acquire reserves an export slot without blocking; callers must invoke the
// returned release exactly once. Export acquires internally — this is for
// callers that need to reserve ahead of time.
func (e *Exporter) Acquire() (release func(), err error) {
	select {
	case e.sem <- struct{}{}:
		return func() { <-e.sem }, nil
	default:
		return nil, ErrBusy
	}
}

// Export encodes frames at the nominal fps into a clip file and returns its
// path plus a cleanup func removing it. Returns ErrBusy without blocking
// when all slots are taken; never blocks capture or display (FR-6 — frames
// are shared slices pinned by this call, no buffer locks held).
func (e *Exporter) Export(ctx context.Context, frames []frame.Frame, fps float64, format Format) (path string, cleanup func(), err error) {
	if len(frames) == 0 {
		return "", nil, errors.New("export: no frames")
	}
	release, err := e.Acquire()
	if err != nil {
		return "", nil, err
	}
	defer release()

	f, err := os.CreateTemp(e.dir, "zeitspiegel-*.mp4")
	if err != nil {
		return "", nil, fmt.Errorf("export: temp file: %w", err)
	}
	path = f.Name()
	f.Close()
	cleanup = func() { os.Remove(path) }

	args := []string{"-v", "error", "-y", "-f", "image2pipe",
		"-framerate", fmt.Sprintf("%g", fps), "-i", "-"}
	switch format {
	case FormatMP4:
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-vf", "scale='min(1280,iw)':-2")
	case FormatMJPEG:
		args = append(args, "-c:v", "copy")
	default:
		cleanup()
		return "", nil, fmt.Errorf("export: unknown format %q", format)
	}
	args = append(args, "-f", "mp4", path)

	cmd := exec.CommandContext(ctx, e.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("export: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("export: start ffmpeg: %w", err)
	}

	writeErr := func() error {
		defer stdin.Close()
		for _, fr := range frames {
			if _, err := stdin.Write(fr.JPEG); err != nil {
				return err
			}
		}
		return nil
	}()
	if err := cmd.Wait(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("export: ffmpeg: %w (%s)", err, stderr.String())
	}
	if writeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("export: feed ffmpeg: %w", writeErr)
	}
	return path, cleanup, nil
}
