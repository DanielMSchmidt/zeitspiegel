package ringbuf_test

// Runtime buffer mutations backing PATCH /api/v1/config (REQUIREMENTS §3:
// profile change ⇒ buffer cleared; buffer_max_s changeable at runtime).

import (
	"errors"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

func TestClear(t *testing.T) {
	b := ringbuf.New(time.Minute, 1<<20)
	for i := range 5 {
		b.Push(mk(uint64(i), time.Duration(i)*time.Second, 100))
	}
	b.Clear()
	st := b.Stats()
	if st.Len != 0 || st.Bytes != 0 {
		t.Errorf("after Clear: len=%d bytes=%d, want 0/0", st.Len, st.Bytes)
	}
	if _, err := b.At(t0); !errors.Is(err, ringbuf.ErrEmpty) {
		t.Errorf("At after Clear: err = %v, want ErrEmpty", err)
	}
	// buffer stays usable
	b.Push(mk(9, 10*time.Second, 100))
	if got := b.Stats().Len; got != 1 {
		t.Errorf("push after Clear: len = %d, want 1", got)
	}
}

func TestSetMaxDuration(t *testing.T) {
	b := ringbuf.New(time.Minute, 1<<20)
	for i := range 11 { // 0..10 s
		b.Push(mk(uint64(i), time.Duration(i)*time.Second, 100))
	}
	b.SetMaxDuration(3 * time.Second) // shrunk budget evicts immediately
	st := b.Stats()
	if st.Span > 3*time.Second {
		t.Errorf("span = %v after shrink, want ≤ 3s", st.Span)
	}
	oldest, err := b.Oldest()
	if err != nil {
		t.Fatal(err)
	}
	if oldest.Seq != 7 {
		t.Errorf("oldest seq = %d, want 7", oldest.Seq)
	}
	if got := b.Stats().MaxDur; got != 3*time.Second {
		t.Errorf("MaxDur = %v, want 3s", got)
	}
}
