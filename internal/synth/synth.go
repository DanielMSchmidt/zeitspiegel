// Package synth provides the synthetic frame source, fake clock and fake
// display used by tests and by `--source synth` demo mode (ARCHITECTURE §D6).
package synth

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// FakeClock is a manually advanced clock satisfying engine.Clock.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a clock frozen at start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{t: start}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Source deterministically generates JPEG frames at an exact frame interval.
// Frame n has CaptureTS = start + n/fps; seq and timestamp are embedded both
// in an APP4 segment (lossless paths) and a pixel pattern that survives
// lossy re-encoding (H.264 exports, IT-4).
type Source struct {
	fps   float64
	start time.Time
	w, h  int
	seq   uint64
}

// NewSource returns a source at the given frame rate; frames are 320×240.
func NewSource(fps float64, start time.Time) *Source {
	return &Source{fps: fps, start: start, w: 320, h: 240}
}

// FPS returns the configured frame rate.
func (s *Source) FPS() float64 { return s.fps }

// SetSeq makes the next frame carry the given sequence number (and the
// timestamp that frame would have had); lets tests start mid-stream without
// generating every preceding frame.
func (s *Source) SetSeq(seq uint64) { s.seq = seq }

// Next generates the next frame. Not safe for concurrent use; the capture
// worker is the only caller (hard rule 5).
func (s *Source) Next() frame.Frame {
	seq := s.seq
	s.seq++
	ts := s.start.Add(time.Duration(float64(seq) * float64(time.Second) / s.fps))
	jpg, err := encodeFrameJPEG(s.w, s.h, seq, ts)
	if err != nil {
		// Encoding a generated in-memory image cannot fail at runtime;
		// treat it as a programming error in test infrastructure.
		panic(fmt.Sprintf("synth: encode frame: %v", err))
	}
	return frame.Frame{Seq: seq, CaptureTS: ts, JPEG: jpg}
}

// seqBits is the width of the pixel-encoded sequence number.
const seqBits = 32

func encodeFrameJPEG(w, h int, seq uint64, ts time.Time) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// white background
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	// seq as 32 black/white blocks across the top quarter, MSB first
	blockW := w / seqBits
	for bit := 0; bit < seqBits; bit++ {
		if seq>>(seqBits-1-bit)&1 == 0 {
			continue
		}
		for x := bit * blockW; x < (bit+1)*blockW; x++ {
			for y := 0; y < h/4; y++ {
				img.Set(x, y, color.Black)
			}
		}
	}
	// moving bar in the lower half (visual feedback in demo mode)
	barX := int(seq*4) % w
	for x := barX; x < barX+8 && x < w; x++ {
		for y := h / 2; y < h; y++ {
			img.Set(x, y, color.RGBA{0, 0, 0xff, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return frame.TagAPP4(buf.Bytes(), seq, ts)
}

// DecodeSeqPixels reads the sequence number back from the block pattern.
// It averages a 3×3 sample per block so the value survives lossy codecs.
func DecodeSeqPixels(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	blockW := w / seqBits
	var seq uint64
	for bit := 0; bit < seqBits; bit++ {
		cx := b.Min.X + bit*blockW + blockW/2
		cy := b.Min.Y + h/8
		var sum, n int
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				r, g, bl, _ := img.At(cx+dx, cy+dy).RGBA()
				sum += int((r + g + bl) / 3 >> 8)
				n++
			}
		}
		if sum/n < 128 { // dark block = bit set
			seq |= 1 << (seqBits - 1 - bit)
		}
	}
	return seq
}

// RenderRecord is one FakeDisplay observation.
type RenderRecord struct {
	Frame      frame.Frame
	RenderedAt time.Time
}

// Clock is the minimal clock dependency of FakeDisplay (satisfied by
// FakeClock and by engine.Clock implementations).
type Clock interface {
	Now() time.Time
}

// FakeDisplay records which frame was rendered when (ARCHITECTURE §D6).
type FakeDisplay struct {
	mu    sync.Mutex
	clock Clock
	recs  []RenderRecord
}

// NewFakeDisplay returns a display stamping records with clock.Now().
func NewFakeDisplay(clock Clock) *FakeDisplay {
	return &FakeDisplay{clock: clock}
}

// Render records the frame; it never fails.
func (d *FakeDisplay) Render(f frame.Frame) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recs = append(d.recs, RenderRecord{Frame: f, RenderedAt: d.clock.Now()})
	return nil
}

// Records returns a copy of all observations so far.
func (d *FakeDisplay) Records() []RenderRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]RenderRecord, len(d.recs))
	copy(out, d.recs)
	return out
}
