package ffcam_test

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/ffcam"
)

// encode produces a clean JPEG like ffmpeg's mjpeg muxer emits (JFIF header,
// no exotic APP segments).
func encode(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// chunkReader yields the stream in fixed-size pieces so frame boundaries
// land mid-chunk, exactly like a pipe would deliver them.
type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(c.n, len(c.data), len(p))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

func TestScannerSplitsFrames(t *testing.T) {
	frames := [][]byte{encode(t, 0), encode(t, 128), encode(t, 255)}
	var stream []byte
	for _, f := range frames {
		stream = append(stream, f...)
	}
	for _, chunk := range []int{1, 7, 4096} { // byte-wise to bigger-than-frame
		s := ffcam.NewScanner(&chunkReader{data: bytes.Clone(stream), n: chunk})
		for i, want := range frames {
			got, err := s.Next()
			if err != nil {
				t.Fatalf("chunk=%d frame %d: %v", chunk, i, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("chunk=%d frame %d: %d bytes, want %d", chunk, i, len(got), len(want))
			}
		}
		if _, err := s.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("chunk=%d: err = %v after last frame, want EOF", chunk, err)
		}
	}
}

func TestScannerSkipsLeadingJunk(t *testing.T) {
	f := encode(t, 50)
	stream := append([]byte("garbage-before-soi"), f...)
	s := ffcam.NewScanner(bytes.NewReader(stream))
	got, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, f) {
		t.Errorf("frame corrupted by junk prefix")
	}
}

func TestScannerTruncatedTail(t *testing.T) {
	f := encode(t, 50)
	s := ffcam.NewScanner(bytes.NewReader(append(f, f[:len(f)/2]...)))
	if _, err := s.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("truncated tail: err = %v, want EOF", err)
	}
}
