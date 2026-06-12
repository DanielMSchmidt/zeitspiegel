package ffcam

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Options configures the ffmpeg invocation. InputArgs select and configure
// the device (everything before the output options), e.g.
//
//	-f avfoundation -framerate 30 -video_size 1280x720 -i default:none
//	-f v4l2 -framerate 30 -video_size 1280x720 -i /dev/video0
//	-f lavfi -i testsrc=size=320x240:rate=30   (tests)
type Options struct {
	FFmpeg    string // binary name/path; empty = "ffmpeg"
	InputArgs []string
	// OutputFPS resamples the output (-r) so the stream matches the
	// profile's nominal rate even when the device delivers fewer fps
	// (frames are duplicated); 0 = no resampling.
	OutputFPS float64
}

// Source implements capture.Source over the ffmpeg subprocess.
type Source struct {
	cmd    *exec.Cmd
	frames chan []byte
	seq    uint64

	mu     sync.Mutex
	stderr strings.Builder // tail for error context
	runErr error
	closed bool
}

// Open starts ffmpeg and begins splitting its MJPEG output. The context
// bounds the subprocess lifetime (the capture supervisor's ctx).
func Open(ctx context.Context, opts Options) (*Source, error) {
	bin := opts.FFmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	args := append([]string{"-v", "error", "-nostdin"}, opts.InputArgs...)
	if opts.OutputFPS > 0 {
		args = append(args, "-r", fmt.Sprintf("%g", opts.OutputFPS))
	}
	args = append(args, "-c:v", "mjpeg", "-q:v", "4", "-f", "mjpeg", "pipe:1")

	s := &Source{frames: make(chan []byte, 2)}
	s.cmd = exec.CommandContext(ctx, bin, args...)
	s.cmd.Stderr = limitedWriter{&s.mu, &s.stderr}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffcam: stdout pipe: %w", err)
	}
	if err := s.cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffcam: start %s: %w", bin, err)
	}

	go func() {
		sc := NewScanner(stdout)
		for {
			jpg, err := sc.Next()
			if err != nil {
				waitErr := s.cmd.Wait()
				s.mu.Lock()
				s.runErr = errors.Join(err, waitErr)
				s.mu.Unlock()
				close(s.frames)
				return
			}
			s.frames <- jpg
		}
	}()
	return s, nil
}

// ReadFrame returns the next frame, stamped with the wall clock at receipt.
func (s *Source) ReadFrame(ctx context.Context) (frame.Frame, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return frame.Frame{}, errors.New("ffcam: source closed")
	}
	select {
	case <-ctx.Done():
		return frame.Frame{}, ctx.Err()
	case jpg, ok := <-s.frames:
		if !ok {
			return frame.Frame{}, fmt.Errorf("ffcam: ffmpeg stream ended: %s", s.failure())
		}
		seq := s.seq
		s.seq++
		return frame.Frame{Seq: seq, CaptureTS: time.Now(), JPEG: jpg}, nil
	}
}

// Close terminates ffmpeg; the reader goroutine drains and exits.
func (s *Source) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
	// Wait happens in the reader goroutine after the pipe closes; drain so
	// it can finish even if nobody reads frames anymore.
	go func() {
		for range s.frames { //nolint:revive // intentional drain
		}
	}()
	return nil
}

func (s *Source) failure() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := strings.TrimSpace(s.stderr.String())
	if s.runErr != nil {
		msg = fmt.Sprintf("%v — %s", s.runErr, msg)
	}
	if msg == "" {
		msg = "no stderr output"
	}
	return msg
}

// limitedWriter keeps the first 4 KiB of stderr under the source's lock.
type limitedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.Len() < 4<<10 {
		w.b.Write(p)
	}
	return len(p), nil
}
