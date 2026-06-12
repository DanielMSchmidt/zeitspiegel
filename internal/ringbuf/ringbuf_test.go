package ringbuf_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// mk builds a frame with the given seq, timestamp offset from t0, and payload size.
func mk(seq uint64, off time.Duration, size int) frame.Frame {
	return frame.Frame{Seq: seq, CaptureTS: t0.Add(off), JPEG: make([]byte, size)}
}

// UT-1: eviction by duration: oldest leaves, order stays monotonic.
func TestEvictionByDuration(t *testing.T) {
	b := ringbuf.New(10*time.Second, 1<<30)
	for i := 0; i <= 11; i++ { // ts 0s..11s, span 11s > 10s after last push
		b.Push(mk(uint64(i), time.Duration(i)*time.Second, 100))
	}
	oldest, err := b.Oldest()
	if err != nil {
		t.Fatalf("Oldest: %v", err)
	}
	if oldest.Seq != 1 {
		t.Errorf("oldest seq = %d, want 1 (frame 0 evicted)", oldest.Seq)
	}
	newest, err := b.Newest()
	if err != nil {
		t.Fatalf("Newest: %v", err)
	}
	if newest.Seq != 11 {
		t.Errorf("newest seq = %d, want 11", newest.Seq)
	}
	if got := b.Stats().Len; got != 11 {
		t.Errorf("len = %d, want 11", got)
	}
	// order monotonic: walk via At at each second
	prev := uint64(0)
	for i := 1; i <= 11; i++ {
		f, err := b.At(t0.Add(time.Duration(i) * time.Second))
		if err != nil {
			t.Fatalf("At(%ds): %v", i, err)
		}
		if f.Seq < prev {
			t.Errorf("order not monotonic: seq %d after %d", f.Seq, prev)
		}
		prev = f.Seq
	}
}

// UT-2: eviction by byte budget kicks in before the duration limit.
func TestEvictionByBytes(t *testing.T) {
	b := ringbuf.New(time.Hour, 1000) // duration effectively unlimited
	for i := 0; i < 5; i++ {          // 5 × 300 B = 1500 B > 1000 B
		b.Push(mk(uint64(i), time.Duration(i)*time.Millisecond, 300))
	}
	st := b.Stats()
	if st.Bytes > 1000 {
		t.Errorf("bytes = %d, want ≤ 1000", st.Bytes)
	}
	if st.Len != 3 {
		t.Errorf("len = %d, want 3 (two oldest evicted)", st.Len)
	}
	oldest, _ := b.Oldest()
	if oldest.Seq != 2 {
		t.Errorf("oldest seq = %d, want 2", oldest.Seq)
	}
}

// UT-3: At: exact hit, in-between, before-first, after-last, empty.
func TestAt(t *testing.T) {
	b := ringbuf.New(time.Minute, 1<<30)
	if _, err := b.At(t0); !errors.Is(err, ringbuf.ErrEmpty) {
		t.Errorf("empty: err = %v, want ErrEmpty", err)
	}
	for i := 0; i < 3; i++ { // frames at 0s, 1s, 2s
		b.Push(mk(uint64(i), time.Duration(i)*time.Second, 10))
	}
	cases := []struct {
		name    string
		t       time.Time
		wantSeq uint64
		wantErr error
	}{
		{"exact hit", t0.Add(1 * time.Second), 1, nil},
		{"in-between", t0.Add(1500 * time.Millisecond), 1, nil},
		{"before-first", t0.Add(-time.Second), 0, ringbuf.ErrTooEarly},
		{"after-last", t0.Add(10 * time.Second), 2, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := b.At(tc.t)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if f.Seq != tc.wantSeq {
				t.Errorf("seq = %d, want %d", f.Seq, tc.wantSeq)
			}
		})
	}
}

// UT-4: property: any insert sequence ⇒ order sorted, budgets never exceeded.
func TestBudgetInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		maxDur := time.Duration(rapid.Int64Range(int64(time.Millisecond), int64(time.Minute)).Draw(rt, "maxDur"))
		maxBytes := rapid.Int64Range(1, 100_000).Draw(rt, "maxBytes")
		b := ringbuf.New(maxDur, maxBytes)
		ts := t0
		n := rapid.IntRange(0, 200).Draw(rt, "n")
		for i := 0; i < n; i++ {
			ts = ts.Add(time.Duration(rapid.Int64Range(0, int64(10*time.Second)).Draw(rt, "delta")))
			b.Push(frame.Frame{Seq: uint64(i), CaptureTS: ts, JPEG: make([]byte, rapid.IntRange(0, 50_000).Draw(rt, "size"))})

			st := b.Stats()
			if st.Len == 0 {
				rt.Fatalf("buffer empty after push")
			}
			// budgets hold unless a single frame alone exceeds them (the just-pushed frame is never evicted)
			if st.Len > 1 {
				if st.Bytes > maxBytes {
					rt.Fatalf("bytes %d > budget %d with %d frames", st.Bytes, maxBytes, st.Len)
				}
				if st.Span > maxDur {
					rt.Fatalf("span %v > budget %v with %d frames", st.Span, maxDur, st.Len)
				}
			}
			oldest, _ := b.Oldest()
			newest, _ := b.Newest()
			if oldest.CaptureTS.After(newest.CaptureTS) {
				rt.Fatalf("order violated: oldest %v after newest %v", oldest.CaptureTS, newest.CaptureTS)
			}
		}
	})
}

// UT-5: one writer + three readers, exercised under -race.
func TestConcurrentReaders(t *testing.T) {
	b := ringbuf.New(time.Second, 1<<20)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if f, err := b.At(t0.Add(500 * time.Millisecond)); err == nil {
					_ = f.JPEG // read the shared slice
				}
				b.Stats()
				b.Oldest()
				b.Newest()
			}
		}()
	}
	for i := 0; i < 5000; i++ { // single writer (hard rule 5)
		b.Push(mk(uint64(i), time.Duration(i)*time.Millisecond, 64))
	}
	close(done)
	wg.Wait()
}
