package framedump_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/framedump"
)

// tickerOf hands the dumper a channel the test fires by hand — the same shape
// httpapi takes for the preview, so nothing here reads a wall clock.
func tickerOf(ch <-chan time.Time) func(time.Duration) (<-chan time.Time, func()) {
	return func(time.Duration) (<-chan time.Time, func()) { return ch, func() {} }
}

// source is a stand-in for the ring buffer's newest frame. Guarded, because
// the real Buffer is: the dumper reads it from its own goroutine while the
// capture side writes.
type source struct {
	mu  sync.Mutex
	f   frame.Frame
	err error
}

func (s *source) set(f frame.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f, s.err = f, nil
}

func (s *source) Newest() (frame.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f, s.err
}

func jpegAt(seq uint64, body string) frame.Frame {
	return frame.Frame{Seq: seq, JPEG: []byte(body)}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// UT-41: a mirror that looks wrong on a TV cannot be diagnosed from a
// description of the colour. A development card writes the frames it is
// actually showing to disk, where `make sd-logs` collects them with everything
// else — no network, no curl, no phone held up to the screen.
func TestDumperWritesOneFramePerTick(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "frames")
	src := &source{f: jpegAt(1, "first")}
	tick := make(chan time.Time)

	d := framedump.New(framedump.Options{Dir: dir, Interval: 5 * time.Second, Keep: 3, Ticker: tickerOf(tick), Source: src})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	for i, body := range []string{"first", "second", "third"} {
		src.set(jpegAt(uint64(i+1), body))
		tick <- time.Time{}
		waitFor(t, func() bool { return len(files(t, dir)) == i+1 })
	}

	got := files(t, dir)
	if len(got) != 3 {
		t.Fatalf("wrote %v, want three frames", got)
	}
	// The sequence number is in the name: it ties a frame to the log line that
	// mentions it, which is the whole point of having both.
	for _, name := range got {
		if !strings.HasSuffix(name, ".jpg") {
			t.Errorf("%q is not a .jpg", name)
		}
	}
	if !strings.Contains(strings.Join(got, " "), "000003") {
		t.Errorf("frame sequence numbers are not in the filenames: %v", got)
	}
}

// UT-41: bounded, because this runs on a card. The newest frames are the ones
// worth keeping — a colour problem is visible in the most recent frame as
// easily as the first.
func TestDumperKeepsOnlyTheNewest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "frames")
	src := &source{f: jpegAt(1, "x")}
	tick := make(chan time.Time)

	d := framedump.New(framedump.Options{Dir: dir, Interval: time.Second, Keep: 2, Ticker: tickerOf(tick), Source: src})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	for seq := 1; seq <= 5; seq++ {
		src.set(jpegAt(uint64(seq), "frame"))
		tick <- time.Time{}
		waitFor(t, func() bool {
			names := files(t, dir)
			return len(names) > 0 && strings.Contains(strings.Join(names, " "), pad(seq))
		})
	}
	waitFor(t, func() bool { return len(files(t, dir)) == 2 })

	got := strings.Join(files(t, dir), " ")
	if strings.Contains(got, pad(1)) || strings.Contains(got, pad(3)) {
		t.Errorf("old frames were not pruned: %v", got)
	}
	for _, seq := range []int{4, 5} {
		if !strings.Contains(got, pad(seq)) {
			t.Errorf("frame %d is missing from %v", seq, got)
		}
	}
}

// UT-41: the capture can stall — a camera unplugged, a buffer never filled.
// Writing the same frame once a tick would fill a card with copies of the
// moment things stopped, and say nothing more than one copy does.
func TestDumperSkipsAFrameItAlreadyWrote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "frames")
	src := &source{f: jpegAt(7, "stuck")}
	tick := make(chan time.Time)

	d := framedump.New(framedump.Options{Dir: dir, Interval: time.Second, Keep: 10, Ticker: tickerOf(tick), Source: src})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	tick <- time.Time{}
	waitFor(t, func() bool { return len(files(t, dir)) == 1 })
	for i := 0; i < 4; i++ {
		tick <- time.Time{}
	}
	// Give the loop room to do the wrong thing if it is going to.
	waitFor(t, func() bool { return d.Ticks() >= 5 })
	if got := files(t, dir); len(got) != 1 {
		t.Errorf("a stalled capture wrote %v, want one frame", got)
	}
}

// UT-41: an empty buffer at boot, or a directory that cannot be written
// (a sealed card, a full disk), must not take the mirror down with it — this
// is a debugging aid, not a dependency.
func TestDumperSurvivesFailures(t *testing.T) {
	t.Run("empty buffer", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "frames")
		src := &source{err: errors.New("ringbuf: empty")}
		tick := make(chan time.Time)
		d := framedump.New(framedump.Options{Dir: dir, Interval: time.Second, Keep: 2, Ticker: tickerOf(tick), Source: src})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go d.Run(ctx)

		tick <- time.Time{}
		waitFor(t, func() bool { return d.Ticks() >= 1 })
		if got := files(t, dir); len(got) != 0 {
			t.Errorf("wrote %v from an empty buffer", got)
		}
	})

	t.Run("unwritable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root, which cannot be locked out of a directory")
		}
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

		src := &source{f: jpegAt(1, "x")}
		tick := make(chan time.Time)
		d := framedump.New(framedump.Options{
			Dir: filepath.Join(parent, "frames"), Interval: time.Second, Keep: 2, Ticker: tickerOf(tick), Source: src,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()

		tick <- time.Time{}
		waitFor(t, func() bool { return d.Ticks() >= 1 })
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after a write failure")
		}
	})
}

func pad(seq int) string {
	return framedump.SeqName(uint64(seq))
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
