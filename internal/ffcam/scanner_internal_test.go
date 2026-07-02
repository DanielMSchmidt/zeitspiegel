package ffcam

import (
	"io"
	"testing"
)

// repeatReader yields n copies of b, then EOF.
type repeatReader struct {
	b []byte
	n int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	r.n--
	return copy(p, r.b), nil
}

// NFR-2: a corrupt stream must not grow the scan buffer without bound.
func TestScannerBoundsBufferOnGarbage(t *testing.T) {
	chunk := make([]byte, 64<<10) // zeros: no SOI anywhere

	// garbage with no frame start at all
	s := NewScanner(&repeatReader{b: chunk, n: 700}) // ~44 MiB
	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
	if len(s.buf) > len(chunk)+1 {
		t.Errorf("no-SOI garbage: buffered %d bytes, want ≤ one chunk", len(s.buf))
	}

	// a frame start whose end never arrives
	s = NewScanner(io.MultiReader(
		&repeatReader{b: soi, n: 1},
		&repeatReader{b: chunk, n: 700},
	))
	if _, err := s.Next(); err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
	if len(s.buf) > maxBuffered {
		t.Errorf("unterminated frame: buffered %d bytes, want ≤ %d", len(s.buf), maxBuffered)
	}
}
