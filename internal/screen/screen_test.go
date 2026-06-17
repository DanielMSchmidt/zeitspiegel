//go:build sdl

package screen_test

// Exercises the real SDL render path (decode → flip → present) under SDL's
// headless "dummy" video driver — runs on any machine with SDL2 libs and in
// the CI hw lane, no display required. This is the same code that drives
// the TV/HDMI output on the appliance.

import (
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
