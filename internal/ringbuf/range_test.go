package ringbuf_test

// Range is the read strategy backing export windowing (TESTPLAN step 4, UT-8).

import (
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

func TestRange(t *testing.T) {
	b := ringbuf.New(time.Minute, 1<<30)
	for i := 0; i < 10; i++ { // 0s..9s
		b.Push(mk(uint64(i), time.Duration(i)*time.Second, 10))
	}
	got := b.Range(t0.Add(2*time.Second), t0.Add(5*time.Second))
	if len(got) != 4 { // 2,3,4,5 — bounds inclusive
		t.Fatalf("len = %d, want 4", len(got))
	}
	if got[0].Seq != 2 || got[3].Seq != 5 {
		t.Errorf("range = [%d..%d], want [2..5]", got[0].Seq, got[3].Seq)
	}
	if r := b.Range(t0.Add(20*time.Second), t0.Add(30*time.Second)); len(r) != 0 {
		t.Errorf("out-of-range query returned %d frames", len(r))
	}
	if r := ringbuf.New(time.Minute, 1<<20).Range(t0, t0.Add(time.Second)); len(r) != 0 {
		t.Errorf("empty buffer returned %d frames", len(r))
	}
}
