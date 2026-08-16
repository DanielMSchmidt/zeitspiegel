//go:build sdl

package screen

// FR-2's acceptance criterion is a pixel comparison, flipped against
// unflipped, and until now nothing performed one: the sdl tests all asserted
// that rendering with a flip set does not *error*, which a renderer ignoring
// the flip entirely would also satisfy. This is the seam between "the API
// accepted the toggle" — proven by UT-43/UT-48 — and "the picture on the TV
// changed", and it is the only part of that chain a machine can check without
// a camera and a person standing in front of it.

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// sidedJPEG encodes a frame that is one colour on the left and another on the
// right — the only kind of picture a horizontal flip is visible in.
func sidedJPEG(t *testing.T, w, h int, left, right color.RGBA) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := left
			if x >= w/2 {
				c = right
			}
			im.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sides reads the renderer's target back and reports whether the left and the
// right of the presented picture are red. Sampling sits a quarter of the way
// in from each edge, well clear of the JPEG's ringing at the colour boundary.
func sides(t *testing.T, s *Screen) (leftRed, rightRed bool) {
	t.Helper()
	w, h, err := s.ren.GetOutputSize()
	if err != nil {
		t.Fatalf("output size: %v", err)
	}
	pix := make([]byte, int(w)*int(h)*4)
	if err := s.ren.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&pix[0]), int(w)*4); err != nil {
		t.Skipf("this SDL renderer cannot read its target back: %v", err)
	}
	at := func(x, y int32) (r, b byte) {
		o := (int(y)*int(w) + int(x)) * 4
		return pix[o+2], pix[o] // ARGB8888 little-endian: B G R A
	}
	lr, lb := at(w/4, h/2)
	rr, rb := at(3*w/4, h/2)
	if lr < 100 && lb < 100 || rr < 100 && rb < 100 {
		t.Fatalf("neither half is a colour we drew (left r=%d b=%d, right r=%d b=%d)", lr, lb, rr, rb)
	}
	return lr > lb, rr > rb
}

// UT-49 (FR-2): the flip actually swaps left and right, and the runtime toggle
// reaches the pixels — on a freshly decoded frame and on a repaint of the one
// already on screen. The repaint case is warm-up: the mirror holds a still
// frame for the whole window (FR-10), and a flip that only took effect on the
// next decode would sit there doing nothing on a unit somebody is standing in
// front of.
func TestMirrorFlipSwapsLeftAndRight(t *testing.T) {
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	s, err := Open(Options{Mirror: false, Windowed: true, Width: 320, Height: 240})
	if err != nil {
		t.Fatalf("Open with dummy driver: %v", err)
	}
	defer s.Close()

	red, blue := color.RGBA{220, 20, 20, 255}, color.RGBA{20, 20, 220, 255}
	f := frame.Frame{Seq: 1, JPEG: sidedJPEG(t, 320, 240, red, blue)}

	if err := s.Render(f); err != nil {
		t.Fatalf("render unflipped: %v", err)
	}
	if l, r := sides(t, s); !l || r {
		t.Fatalf("unflipped: left red = %v, right red = %v — want the picture as the camera sent it", l, r)
	}

	s.SetMirror(true)
	if err := s.Render(frame.Frame{Seq: 2, JPEG: f.JPEG}); err != nil {
		t.Fatalf("render flipped: %v", err)
	}
	if l, r := sides(t, s); l || !r {
		t.Fatalf("flipped: left red = %v, right red = %v — the flip did not reach the picture (FR-2)", l, r)
	}

	// Back off again, without a new frame: a held tick's repaint has to carry
	// the current flip, not the one the last decode was drawn with.
	s.SetMirror(false)
	if err := s.Repaint(); err != nil {
		t.Fatalf("repaint: %v", err)
	}
	if l, r := sides(t, s); !l || r {
		t.Errorf("repaint after unflipping: left red = %v, right red = %v — a repaint kept the old flip", l, r)
	}
}
