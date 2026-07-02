package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// FR-8/FR-10: /status warming_up must follow the engine's semantics — an
// empty buffer is warming up, and so is a delay target before the oldest
// frame; a satisfiable target is not.
func TestStatusWarmingUpMatchesEngine(t *testing.T) {
	newStatus := func(buf *ringbuf.Buffer, delay time.Duration) *sysStatus {
		cfg := config.Default()
		eng := engine.New(buf)
		eng.SetDelay(delay)
		return &sysStatus{
			start: time.Now(),
			cfg:   cfg,
			store: &runtimeStore{rt: cfg.Runtime(), buf: buf, restart: &atomic.Bool{}},
			buf:   buf,
			eng:   eng,
			sup:   capture.New(capture.Options{}),
		}
	}

	empty := ringbuf.New(time.Minute, 1<<20)
	if st := newStatus(empty, 0).Status(); !st.WarmingUp {
		t.Error("empty buffer with delay 0: warming_up = false, want true (nothing to display)")
	}

	filled := ringbuf.New(time.Minute, 1<<20)
	filled.Push(frame.Frame{Seq: 0, CaptureTS: time.Now().Add(-time.Second)})
	filled.Push(frame.Frame{Seq: 1, CaptureTS: time.Now()})
	if st := newStatus(filled, 0).Status(); st.WarmingUp {
		t.Error("satisfiable delay: warming_up = true, want false")
	}
	if st := newStatus(filled, time.Hour).Status(); !st.WarmingUp {
		t.Error("delay beyond oldest frame: warming_up = false, want true")
	}
}
