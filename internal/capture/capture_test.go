package capture_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// burstSource emits n frames (globally increasing seq), then fails.
type burstSource struct {
	seq    *atomic.Uint64
	left   int
	closed atomic.Bool
}

func (b *burstSource) ReadFrame(ctx context.Context) (frame.Frame, error) {
	if err := ctx.Err(); err != nil {
		return frame.Frame{}, err
	}
	if b.left == 0 {
		return frame.Frame{}, errors.New("usb device gone")
	}
	b.left--
	s := b.seq.Add(1) - 1
	return frame.Frame{Seq: s, CaptureTS: t0.Add(time.Duration(s) * time.Millisecond)}, nil
}

func (b *burstSource) Close() error { b.closed.Store(true); return nil }

// IT-7 / NFR-5: source error ⇒ reconnect with growing backoff, status
// degraded during the outage, healthy afterwards, no crash.
func TestReconnectWithBackoff(t *testing.T) {
	var seq atomic.Uint64
	var opens int
	var slept []time.Duration
	var degradedDuringSleep []bool

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sup *capture.Supervisor
	var pushed atomic.Int64
	var degradedAtEnd atomic.Bool

	sources := []*burstSource{
		{seq: &seq, left: 5},       // 5 good frames, then error
		{seq: &seq, left: 1 << 30}, // healthy after reconnect: emits until canceled
	}
	var reported []string
	sup = capture.New(capture.Options{
		OnError: func(err error) { reported = append(reported, err.Error()) },
		Open: func(ctx context.Context) (capture.Source, error) {
			opens++
			switch opens {
			case 1:
				return sources[0], nil
			case 2:
				return nil, errors.New("open: device busy") // one failed reopen
			default:
				return sources[1], nil
			}
		},
		Push: func(f frame.Frame) {
			if n := pushed.Add(1); n == 50 {
				degradedAtEnd.Store(sup.Degraded())
				cancel() // enough frames seen from the reconnected source
			}
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			degradedDuringSleep = append(degradedDuringSleep, sup.Degraded())
			return nil
		},
	})

	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}

	if opens < 3 {
		t.Errorf("opens = %d, want ≥ 3 (initial, failed reopen, successful reopen)", opens)
	}
	if len(slept) < 2 {
		t.Fatalf("slept %v, want ≥ 2 backoff sleeps", slept)
	}
	if slept[1] <= slept[0] {
		t.Errorf("backoff not growing: %v", slept)
	}
	for i, d := range degradedDuringSleep {
		if !d {
			t.Errorf("sleep %d: not degraded during outage", i)
		}
	}
	if degradedAtEnd.Load() {
		t.Error("still degraded after healthy frames flowed")
	}
	if !sources[0].closed.Load() {
		t.Error("failed source was not closed")
	}
	if pushed.Load() < 50 {
		t.Errorf("pushed = %d, want ≥ 50", pushed.Load())
	}
	// outage causes must surface for diagnosis (OnError → slog in cmd)
	if len(reported) < 2 {
		t.Fatalf("OnError reported %v, want the read error and the open error", reported)
	}
	if !strings.Contains(strings.Join(reported, " "), "usb device gone") ||
		!strings.Contains(strings.Join(reported, " "), "device busy") {
		t.Errorf("OnError missing causes: %v", reported)
	}
}

// The default backoff grows and is capped (pure function).
func TestDefaultBackoff(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 0; attempt < 10; attempt++ {
		d := capture.DefaultBackoff(attempt)
		if d <= 0 {
			t.Fatalf("backoff(%d) = %v", attempt, d)
		}
		if d < prev {
			t.Fatalf("backoff shrank at attempt %d: %v < %v", attempt, d, prev)
		}
		prev = d
	}
	if max := capture.DefaultBackoff(20); max > 5*time.Second {
		t.Errorf("backoff(20) = %v, want capped ≤ 5s", max)
	}
}

// Overflow: when the push side stalls, the worker drops instead of blocking
// the read side (ARCHITECTURE §4: queue 4, drop + count).
func TestDropsWhenPushStalls(t *testing.T) {
	var seq atomic.Uint64
	release := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sup *capture.Supervisor
	emitted := make(chan struct{})
	sup = capture.New(capture.Options{
		Open: func(ctx context.Context) (capture.Source, error) {
			return &slowDrain{seq: &seq, n: 64, done: emitted}, nil
		},
		Push: func(f frame.Frame) {
			once.Do(func() { <-release }) // first push blocks until released
		},
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	go func() {
		<-emitted // all 64 frames read by the worker
		close(release)
		cancel()
	}()
	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := sup.Dropped(); d == 0 {
		t.Error("Dropped() = 0, want > 0 while push side stalled")
	}
}

// ARCHITECTURE §4: on overflow the *oldest* queued frame is dropped so the
// mirror stays as fresh as possible — the newest frame always survives.
func TestDropOldestKeepsNewest(t *testing.T) {
	var seq atomic.Uint64
	release := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var pushed []uint64
	emitted := make(chan struct{})
	sup := capture.New(capture.Options{
		Open: func(ctx context.Context) (capture.Source, error) {
			return &slowDrain{seq: &seq, n: 10, done: emitted}, nil
		},
		Push: func(f frame.Frame) {
			once.Do(func() { <-release }) // first push blocks until released
			mu.Lock()
			pushed = append(pushed, f.Seq)
			last := f.Seq
			mu.Unlock()
			if last == 9 { // the newest frame arrived: done
				cancel()
			}
		},
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	go func() {
		<-emitted // all 10 frames read by the worker; the queue overflowed
		close(release)
		// Old drop-newest behavior never delivers seq 9; don't hang forever.
		time.Sleep(2 * time.Second)
		cancel()
	}()
	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if d := sup.Dropped(); d == 0 {
		t.Error("Dropped() = 0, want > 0 while push side stalled")
	}
	mu.Lock()
	defer mu.Unlock()
	got := make(map[uint64]bool, len(pushed))
	for _, s := range pushed {
		got[s] = true
	}
	if !got[9] {
		t.Errorf("pushed %v: newest frame (seq 9) was dropped — overflow must drop the oldest", pushed)
	}
}

// NFR-5: a device that opens fine but fails every read (half-dead USB,
// metadata-only node) must back off with growing pauses, not churn at the
// initial backoff forever.
func TestBackoffGrowsAcrossReopens(t *testing.T) {
	var slept []time.Duration
	sup := capture.New(capture.Options{
		Open: func(ctx context.Context) (capture.Source, error) {
			return &burstSource{seq: &atomic.Uint64{}, left: 0}, nil // reads fail instantly
		},
		Push: func(frame.Frame) {},
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			if len(slept) >= 5 {
				return context.Canceled // stop the supervisor
			}
			return nil
		},
	})
	if err := sup.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(slept) < 5 {
		t.Fatalf("slept %v, want 5 recorded backoffs", slept)
	}
	if slept[4] <= slept[0] {
		t.Errorf("backoff not growing across open-then-read-fail cycles: %v", slept)
	}
}

// A deliberate pipeline restart (config change, FR-9) reopens the source
// immediately: no backoff pause, no error report, never degraded.
func TestRestartReopensCleanly(t *testing.T) {
	var seq atomic.Uint64
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sup *capture.Supervisor
	var opens int
	var slept []time.Duration
	var reported []string
	var pushed atomic.Int64
	var degradedSeen atomic.Bool

	first := &restartAfter{seq: &seq, n: 2}
	sup = capture.New(capture.Options{
		OnError: func(err error) { reported = append(reported, err.Error()) },
		Open: func(ctx context.Context) (capture.Source, error) {
			opens++
			if opens == 1 {
				return first, nil
			}
			return &burstSource{seq: &seq, left: 1 << 30}, nil
		},
		Push: func(f frame.Frame) {
			if sup.Degraded() {
				degradedSeen.Store(true)
			}
			if pushed.Add(1) == 10 {
				cancel()
			}
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})
	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if opens != 2 {
		t.Errorf("opens = %d, want 2 (initial + restart)", opens)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v, want no backoff on a deliberate restart", slept)
	}
	if len(reported) != 0 {
		t.Errorf("OnError reported %v, want nothing for a deliberate restart", reported)
	}
	if degradedSeen.Load() {
		t.Error("supervisor reported degraded during a deliberate restart")
	}
	if !first.closed.Load() {
		t.Error("restarted source was not closed")
	}
}

// restartAfter emits n frames, then requests a deliberate restart.
type restartAfter struct {
	seq    *atomic.Uint64
	n      int
	closed atomic.Bool
}

func (r *restartAfter) ReadFrame(ctx context.Context) (frame.Frame, error) {
	if err := ctx.Err(); err != nil {
		return frame.Frame{}, err
	}
	if r.n == 0 {
		return frame.Frame{}, capture.ErrRestart
	}
	r.n--
	s := r.seq.Add(1) - 1
	return frame.Frame{Seq: s, CaptureTS: t0.Add(time.Duration(s) * time.Millisecond)}, nil
}

func (r *restartAfter) Close() error { r.closed.Store(true); return nil }

// slowDrain emits n frames then blocks until ctx is done, closing done after
// the last emit.
type slowDrain struct {
	seq  *atomic.Uint64
	n    int
	done chan struct{}
}

func (s *slowDrain) ReadFrame(ctx context.Context) (frame.Frame, error) {
	if s.n == 0 {
		close(s.done)
		<-ctx.Done()
		return frame.Frame{}, ctx.Err()
	}
	s.n--
	sq := s.seq.Add(1) - 1
	return frame.Frame{Seq: sq, CaptureTS: t0.Add(time.Duration(sq) * time.Millisecond)}, nil
}

func (s *slowDrain) Close() error { return nil }

// scriptSource emits a fixed list of frames, then fails (triggering a reopen).
type scriptSource struct {
	frames []frame.Frame
	i      int
}

func (s *scriptSource) ReadFrame(ctx context.Context) (frame.Frame, error) {
	if err := ctx.Err(); err != nil {
		return frame.Frame{}, err
	}
	if s.i == len(s.frames) {
		return frame.Frame{}, errors.New("usb device gone")
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}

func (s *scriptSource) Close() error { return nil }

// UT-14: a CaptureTS delta above GapThreshold increments Gaps() and fires
// OnGap; a source reopen does not count as a gap (it is already reported via
// OnError); MaxFrameBytes tracks the largest payload seen.
func TestGapDetectionAndMaxFrameBytes(t *testing.T) {
	const interval = 33 * time.Millisecond
	mk := func(seq uint64, ts time.Duration, size int) frame.Frame {
		return frame.Frame{Seq: seq, CaptureTS: t0.Add(ts), JPEG: make([]byte, size)}
	}
	sources := []*scriptSource{
		{frames: []frame.Frame{
			mk(0, 0, 100),
			mk(1, interval, 300),
			mk(2, 8*interval, 200), // hole: 7 intervals missing
			mk(3, 9*interval, 100),
		}},
		// reopen lands far in the future: must NOT count as a gap
		{frames: []frame.Frame{
			mk(0, 10*time.Second, 100),
			mk(1, 10*time.Second+interval, 100),
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var gapDeltas []time.Duration
	var pushed atomic.Int64
	opens := 0
	sup := capture.New(capture.Options{
		Open: func(ctx context.Context) (capture.Source, error) {
			if opens == len(sources) {
				<-ctx.Done() // script exhausted: park until the test cancels
				return nil, ctx.Err()
			}
			s := sources[opens]
			opens++
			return s, nil
		},
		Push: func(f frame.Frame) {
			if pushed.Add(1) == 6 {
				cancel()
			}
		},
		Sleep:        func(ctx context.Context, d time.Duration) error { return nil },
		GapThreshold: func() time.Duration { return 2 * interval },
		OnGap:        func(delta time.Duration) { gapDeltas = append(gapDeltas, delta) },
	})

	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}

	if got := sup.Gaps(); got != 1 {
		t.Errorf("Gaps() = %d, want 1 (the in-stream hole; not the reopen jump)", got)
	}
	if len(gapDeltas) != 1 || gapDeltas[0] != 7*interval {
		t.Errorf("OnGap deltas = %v, want [%v]", gapDeltas, 7*interval)
	}
	if got := sup.MaxFrameBytes(); got != 300 {
		t.Errorf("MaxFrameBytes() = %d, want 300", got)
	}
}
