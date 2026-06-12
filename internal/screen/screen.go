//go:build sdl

// Package screen is the thin SDL2/KMSDRM display adapter (ARCHITECTURE §D3):
// full-screen rendering straight to HDMI, no X11. cgo lives only here and in
// internal/camera (hard rule 2).
package screen

import (
	"fmt"
	"sync/atomic"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Screen implements engine.Display. All methods must run on the main OS
// thread (cmd locks it; SDL requirement).
type Screen struct {
	win    *sdl.Window
	ren    *sdl.Renderer
	mirror atomic.Bool
}

// Open initializes SDL video and a borderless full-screen renderer with
// vsync. mirror enables horizontal flip (FR-2, default on).
func Open(mirror bool) (*Screen, error) {
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		return nil, fmt.Errorf("screen: sdl init: %w", err)
	}
	if err := img.Init(img.INIT_JPG); err != nil {
		sdl.Quit()
		return nil, fmt.Errorf("screen: sdl_image init: %w", err)
	}
	sdl.ShowCursor(sdl.DISABLE)
	win, err := sdl.CreateWindow("zeitspiegel",
		sdl.WINDOWPOS_UNDEFINED, sdl.WINDOWPOS_UNDEFINED, 0, 0,
		sdl.WINDOW_FULLSCREEN_DESKTOP|sdl.WINDOW_SHOWN)
	if err != nil {
		img.Quit()
		sdl.Quit()
		return nil, fmt.Errorf("screen: create window: %w", err)
	}
	ren, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_ACCELERATED|sdl.RENDERER_PRESENTVSYNC)
	if err != nil {
		win.Destroy()
		img.Quit()
		sdl.Quit()
		return nil, fmt.Errorf("screen: create renderer: %w", err)
	}
	s := &Screen{win: win, ren: ren}
	s.mirror.Store(mirror)
	return s, nil
}

// SetMirror toggles horizontal flip at runtime (PATCH /config).
func (s *Screen) SetMirror(on bool) { s.mirror.Store(on) }

// Render decodes one JPEG (SDL2_image / libjpeg-turbo) and presents it
// full-screen, flipped when mirroring. Budget: decode 4–8 ms + present
// 2–4 ms at 720p on the Pi 5 (validate in spike S-1).
func (s *Screen) Render(f frame.Frame) error {
	rw, err := sdl.RWFromMem(f.JPEG)
	if err != nil {
		return fmt.Errorf("screen: rwops: %w", err)
	}
	surf, err := img.LoadRW(rw, true)
	if err != nil {
		return fmt.Errorf("screen: decode jpeg (seq %d): %w", f.Seq, err)
	}
	defer surf.Free()
	tex, err := s.ren.CreateTextureFromSurface(surf)
	if err != nil {
		return fmt.Errorf("screen: texture: %w", err)
	}
	defer tex.Destroy()

	flip := sdl.FLIP_NONE
	if s.mirror.Load() {
		flip = sdl.FLIP_HORIZONTAL
	}
	if err := s.ren.Clear(); err != nil {
		return fmt.Errorf("screen: clear: %w", err)
	}
	if err := s.ren.CopyEx(tex, nil, nil, 0, nil, flip); err != nil {
		return fmt.Errorf("screen: copy: %w", err)
	}
	s.ren.Present()
	return nil
}

// Close tears down SDL.
func (s *Screen) Close() error {
	s.ren.Destroy()
	s.win.Destroy()
	img.Quit()
	sdl.Quit()
	return nil
}
