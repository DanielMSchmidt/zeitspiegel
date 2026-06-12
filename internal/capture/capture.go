// Package capture owns the buffer write path: one worker reads a frame
// source, queues into a small channel (drop-on-overflow — capture never
// blocks, ARCHITECTURE §4) and a single pusher goroutine calls Buffer.Push
// (hard rule 5). A supervisor reopens the source with backoff on errors
// (NFR-5). Timing is injected; no wall clock here (hard rule 6).
package capture

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Source delivers frames. internal/camera implements it on hardware; tests
// and demo mode use synthetic sources.
type Source interface {
	ReadFrame(ctx context.Context) (frame.Frame, error)
	Close() error
}

// DefaultBackoff doubles from 100 ms, capped at 3 s.
func DefaultBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond << min(attempt, 5)
	if d > 3*time.Second {
		return 3 * time.Second
	}
	return d
}

// Options wires a Supervisor.
type Options struct {
	// Open creates (or reopens) the source.
	Open func(ctx context.Context) (Source, error)
	// Push receives every captured frame; the supervisor guarantees a
	// single calling goroutine.
	Push func(frame.Frame)
	// Sleep pauses between reconnect attempts (real timer in cmd,
	// recorded in tests).
	Sleep func(ctx context.Context, d time.Duration) error
	// Backoff maps the consecutive failure count to a pause; nil =
	// DefaultBackoff.
	Backoff func(attempt int) time.Duration
	// QueueLen is the read→push channel depth; 0 = 4 (ARCHITECTURE §4).
	QueueLen int
}

// Supervisor runs the capture loop and republishes its health.
type Supervisor struct {
	o        Options
	degraded atomic.Bool
	dropped  atomic.Uint64
}

// New validates nothing fancy — zero-value options fields get defaults.
func New(o Options) *Supervisor {
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	if o.QueueLen == 0 {
		o.QueueLen = 4
	}
	return &Supervisor{o: o}
}

// Degraded reports whether the source is currently down (status endpoint).
func (s *Supervisor) Degraded() bool { return s.degraded.Load() }

// Dropped counts frames discarded because the push side stalled.
func (s *Supervisor) Dropped() uint64 { return s.dropped.Load() }

// Run captures until ctx is done; that is the only nil return. Source
// errors never propagate — they degrade and reconnect.
func (s *Supervisor) Run(ctx context.Context) error {
	queue := make(chan frame.Frame, s.o.QueueLen)
	pushDone := make(chan struct{})
	go func() { // sole Push caller
		defer close(pushDone)
		for f := range queue {
			s.o.Push(f)
		}
	}()
	defer func() { close(queue); <-pushDone }()

	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		src, err := s.o.Open(ctx)
		if err != nil {
			s.degraded.Store(true)
			if err := s.o.Sleep(ctx, s.o.Backoff(attempt)); err != nil {
				return nil
			}
			attempt++
			continue
		}
		s.degraded.Store(false)
		attempt = 0
		for {
			f, err := src.ReadFrame(ctx)
			if err != nil {
				src.Close()
				if ctx.Err() != nil {
					return nil
				}
				s.degraded.Store(true)
				if err := s.o.Sleep(ctx, s.o.Backoff(attempt)); err != nil {
					return nil
				}
				attempt++
				break
			}
			select {
			case queue <- f:
			default:
				s.dropped.Add(1) // never block the read side (FR-6/NFR-1)
			}
		}
	}
}
