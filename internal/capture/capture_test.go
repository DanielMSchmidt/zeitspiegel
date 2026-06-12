package capture_test

import (
	"context"
	"errors"
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
	sup = capture.New(capture.Options{
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
