//go:build sdl

package screen_test

// Exercises the real SDL render path (decode → flip → present) under SDL's
// headless "dummy" video driver — runs on any machine with SDL2 libs and in
// the CI hw lane, no display required. This is the same code that drives
// the TV/HDMI output on the appliance.

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/screen"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

var _ engine.Display = (*screen.Screen)(nil)

func openDummy(t *testing.T) *screen.Screen {
	t.Helper()
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	s, err := screen.Open(screen.Options{Mirror: true, Windowed: true, Width: 320, Height: 240})
	if err != nil {
		t.Fatalf("Open with dummy driver: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRenderDecodesAndPresents(t *testing.T) {
	s := openDummy(t)
	src := synth.NewSource(30, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	for i := 0; i < 3; i++ {
		if err := s.Render(src.Next()); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	s.SetMirror(false) // runtime toggle must not break rendering
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("after mirror toggle: %v", err)
	}
}

func TestRenderRejectsGarbage(t *testing.T) {
	s := openDummy(t)
	if err := s.Render(frame.Frame{Seq: 9, JPEG: []byte("not a jpeg")}); err == nil {
		t.Error("garbage frame must return an error, not crash")
	}
}

func TestProcessEventsQuietByDefault(t *testing.T) {
	s := openDummy(t)
	if quit := s.ProcessEvents(); quit {
		t.Error("no events pending, ProcessEvents must not report quit")
	}
}

// Splash must paint without a frame: covers the warm-up window between
// SDL open and the first camera capture. Exercises both before any real
// Render (the boot path) and after (defensive — no-op'd by caller).
func TestSplashPaintsWithoutFrame(t *testing.T) {
	s := openDummy(t)
	s.SetDelay(15 * time.Second)
	if err := s.Splash(); err != nil {
		t.Fatalf("splash on fresh display: %v", err)
	}
	// Real frame after splash still works (no SDL state corruption).
	src := synth.NewSource(30, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render after splash: %v", err)
	}
}

// UT-11 (sdl side): SetDelay before Render must not affect rendering and
// the glyph atlas must have loaded successfully in Open.
func TestRenderWithDelayBadge(t *testing.T) {
	s := openDummy(t)
	src := synth.NewSource(30, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	s.SetDelay(0)
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at delay 0: %v", err)
	}
	s.SetDelay(90 * time.Second)
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at delay 90s: %v", err)
	}
	s.SetDelay(-1 * time.Second) // clamp path
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at negative delay: %v", err)
	}
}

// encodeJPEG produces a solid-colour JPEG at the given size (stdlib only).
func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range im.Pix {
		im.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, im, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// UT-15: the frame texture is created once and reused across same-size
// frames; a dimension change recreates it exactly once; Info reports the
// renderer that Open actually got (the silent software fallback must be
// visible to the caller).
func TestFrameTextureReuseAndInfo(t *testing.T) {
	s := openDummy(t)
	src := synth.NewSource(30, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	for i := 0; i < 3; i++ {
		if err := s.Render(src.Next()); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if got := s.TextureRecreates(); got != 1 {
		t.Fatalf("after 3 same-size frames: TextureRecreates() = %d, want 1", got)
	}
	if err := s.Render(frame.Frame{Seq: 100, JPEG: encodeJPEG(t, 640, 480)}); err != nil {
		t.Fatalf("render 640x480: %v", err)
	}
	if got := s.TextureRecreates(); got != 2 {
		t.Fatalf("after size change: TextureRecreates() = %d, want 2", got)
	}
	if info := s.Info(); info.Renderer == "" {
		t.Error("Info().Renderer is empty, want the SDL renderer name")
	}
}
