// Package export turns a frame window into a downloadable clip by piping
// JPEGs through an ffmpeg subprocess (ARCHITECTURE §D4). ffmpeg is always a
// subprocess, never a linked library (hard rule 7). Output is fragmented
// MP4 streamed from ffmpeg's stdout — the consumer receives the first
// container bytes while the encode is still running.
package export

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// Exporter runs at most `slots` concurrent ffmpeg exports (1 in production —
// x264 on the Pi's four cores must not starve the render loop; the slot
// semaphore itself is exercised with 3 in TESTPLAN IT-8). A slot is held
// from Prepare until the stream finishes, so it also bounds concurrent
// downloads.
type Exporter struct {
	ffmpeg string
	sem    chan struct{}
	// Nice is the Unix niceness applied to each ffmpeg process right after
	// start (0 = inherit). Exports are guest-facing bulk work; the render
	// loop's 16.7 ms tick budget is not.
	Nice int
}

// New creates an exporter with the given number of concurrent slots.
func New(slots int) *Exporter {
	return &Exporter{ffmpeg: "ffmpeg", sem: make(chan struct{}, slots)}
}

// Acquire reserves an export slot without blocking; callers must invoke the
// returned release exactly once. Prepare acquires internally — this is for
// callers that need to reserve ahead of time.
func (e *Exporter) Acquire() (release func(), err error) {
	select {
	case e.sem <- struct{}{}:
		return func() { <-e.sem }, nil
	default:
		return nil, ErrBusy
	}
}

// Stream is a prepared encode holding one export slot. Exactly one of
// WriteTo (which streams and then releases the slot) or Close (which
// releases the slot unused) must run it to completion; Close after WriteTo
// is a no-op.
type Stream struct {
	e       *Exporter
	ctx     context.Context
	frames  []frame.Frame
	fps     float64
	format  Format
	release func()
}

// Prepare validates inputs and reserves an export slot without blocking
// (ErrBusy when all slots are taken — never blocks capture or display,
// FR-6). ffmpeg has not started yet; the frames are shared slices pinned by
// the returned Stream until it finishes (no buffer locks held).
func (e *Exporter) Prepare(ctx context.Context, frames []frame.Frame, fps float64, format Format) (*Stream, error) {
	if len(frames) == 0 {
		return nil, errors.New("export: no frames")
	}
	if format != FormatMP4 && format != FormatMJPEG {
		return nil, fmt.Errorf("export: unknown format %q", format)
	}
	release, err := e.Acquire()
	if err != nil {
		return nil, err
	}
	return &Stream{e: e, ctx: ctx, frames: frames, fps: fps, format: format, release: release}, nil
}

// Close releases the slot if WriteTo never ran; idempotent.
func (s *Stream) Close() error {
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return nil
}

// WriteTo runs ffmpeg and copies its stdout to w as it is produced,
// returning the number of body bytes written. The slot is released on
// return. Call at most once. Cancelling the Prepare context (client
// disconnect) or a failing w both kill ffmpeg and return promptly.
func (s *Stream) WriteTo(w io.Writer) (int64, error) {
	defer s.Close()

	// empty_moov emits a sample-table-free header immediately (a plain
	// `-f mp4` refuses non-seekable output); default_base_moof keeps offsets
	// moof-relative, the CMAF convention. 0.5 s fragments keep bytes flowing
	// regardless of x264's keyframe placement (its default GOP is 250
	// frames — frag_keyframe alone would hold the first fragment for ~8 s
	// of media).
	args := []string{"-v", "error", "-f", "image2pipe", "-vcodec", "mjpeg",
		"-framerate", fmt.Sprintf("%g", s.fps), "-i", "-"}
	switch s.format {
	case FormatMP4:
		args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-vf", "scale='min(1280,iw)':-2")
	case FormatMJPEG:
		args = append(args, "-c:v", "copy")
	}
	args = append(args,
		"-movflags", "empty_moov+default_base_moof", "-frag_duration", "500000",
		"-f", "mp4", "pipe:1")

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.e.ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, fmt.Errorf("export: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("export: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("export: start ffmpeg: %w", err)
	}
	// Best-effort: a failed renice leaves the export at normal priority,
	// which is no worse than not trying.
	_ = applyNice(cmd.Process.Pid, s.e.Nice)

	// Feed stdin concurrently: writing all frames up front would deadlock
	// once ffmpeg fills the stdout pipe waiting for us to read it. An EPIPE
	// here after a downstream abort is expected, not an error in itself.
	feedErr := make(chan error, 1)
	go func() {
		defer stdin.Close()
		for _, fr := range s.frames {
			if _, err := stdin.Write(fr.JPEG); err != nil {
				feedErr <- err
				return
			}
		}
		feedErr <- nil
	}()

	n, copyErr := io.Copy(w, stdout)
	if copyErr != nil {
		cancel() // kill ffmpeg; unblocks the feeder too
	}
	waitErr := cmd.Wait()
	fErr := <-feedErr

	// Error priority: destination failure (client abort / write deadline)
	// over ffmpeg failure over feed failure — the earlier ones cause the
	// later ones.
	switch {
	case copyErr != nil:
		return n, fmt.Errorf("export: stream: %w", copyErr)
	case s.ctx.Err() != nil:
		return n, fmt.Errorf("export: aborted: %w", s.ctx.Err())
	case waitErr != nil:
		return n, fmt.Errorf("export: ffmpeg: %w (%s)", waitErr, stderr.Bytes())
	case fErr != nil:
		return n, fmt.Errorf("export: feed ffmpeg: %w", fErr)
	}
	return n, nil
}
