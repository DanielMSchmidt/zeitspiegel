//go:build sdl

// Package screen is the thin SDL2/KMSDRM display adapter (ARCHITECTURE §D3):
// full-screen rendering straight to HDMI, no X11. cgo lives only here and in
// internal/camera (hard rule 2).
package screen

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Screen implements engine.Display. All methods must run on the main OS
// thread (cmd locks it; SDL requirement).
type Screen struct {
	win      *sdl.Window
	ren      *sdl.Renderer
	mirror   atomic.Bool
	delayNS  atomic.Int64
	glyphTex *sdl.Texture
	info     Info

	// frameTex is the persistent streaming texture frames decode into.
	// Creating and destroying a GPU texture per frame causes driver-side
	// allocation churn and periodic hitches on KMSDRM/GLES; reusing one
	// texture and Update()ing its pixels is the steady-state path. It is
	// recreated only when the frame dimensions or pixel format change
	// (profile switch).
	frameTex     *sdl.Texture
	texW, texH   int32
	texFmt       uint32
	texRecreates atomic.Uint64
}

// Info describes what Open actually negotiated with SDL. The render loop
// logs it: a software renderer or a non-60 Hz display each explain visible
// judder on their own.
type Info struct {
	Renderer  string // SDL renderer backend name (e.g. "opengles2", "software")
	Software  bool   // true = software fallback; blows the render budget on the Pi
	RefreshHz int    // current display refresh rate; 0 = unknown
}

// Info reports the negotiated renderer and display mode.
func (s *Screen) Info() Info { return s.info }

// TextureRecreates counts frame-texture (re)creations. Steady state is 1;
// it climbing with every frame means the streaming path is unavailable and
// rendering fell back to per-frame texture uploads.
func (s *Screen) TextureRecreates() uint64 { return s.texRecreates.Load() }

// SetDelay stores the delay shown by the on-screen badge. Called from the
// render loop each tick; no locking needed (single writer in practice).
func (s *Screen) SetDelay(d time.Duration) { s.delayNS.Store(int64(d)) }

// Options configures the display.
type Options struct {
	Mirror bool // horizontal flip (FR-2, default on in config)
	// Windowed renders into a desktop window instead of taking the whole
	// display — the dev "what would the TV show" mode. The appliance runs
	// fullscreen (KMSDRM has nothing else to show anyway).
	Windowed      bool
	Width, Height int // window size when Windowed; 0 = 1280×720
}

// Open initializes SDL video and a renderer with vsync; on the appliance a
// borderless full-screen one.
func Open(o Options) (*Screen, error) {
	if err := sdl.Init(sdl.INIT_VIDEO); err != nil {
		return nil, fmt.Errorf("screen: sdl init: %w", err)
	}
	if err := img.Init(img.INIT_JPG | img.INIT_PNG); err != nil {
		sdl.Quit()
		return nil, fmt.Errorf("screen: sdl_image init: %w", err)
	}
	w, h, flags := int32(0), int32(0), uint32(sdl.WINDOW_FULLSCREEN_DESKTOP|sdl.WINDOW_SHOWN)
	if o.Windowed {
		w, h, flags = 1280, 720, sdl.WINDOW_SHOWN
		if o.Width > 0 && o.Height > 0 {
			w, h = int32(o.Width), int32(o.Height)
		}
	} else {
		sdl.ShowCursor(sdl.DISABLE)
	}
	win, err := sdl.CreateWindow("zeitspiegel",
		sdl.WINDOWPOS_UNDEFINED, sdl.WINDOWPOS_UNDEFINED, w, h, flags)
	if err != nil {
		img.Quit()
		sdl.Quit()
		return nil, fmt.Errorf("screen: create window: %w", err)
	}
	ren, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_ACCELERATED|sdl.RENDERER_PRESENTVSYNC)
	if err != nil {
		// headless/dummy drivers and odd GPUs: software rendering still
		// exercises the identical decode/flip/present path
		ren, err = sdl.CreateRenderer(win, -1, sdl.RENDERER_SOFTWARE)
	}
	if err != nil {
		win.Destroy()
		img.Quit()
		sdl.Quit()
		return nil, fmt.Errorf("screen: create renderer: %w", err)
	}
	s := &Screen{win: win, ren: ren}
	s.mirror.Store(o.Mirror)
	if ri, err := ren.GetInfo(); err == nil {
		s.info.Renderer = ri.Name
		s.info.Software = ri.Flags&sdl.RENDERER_SOFTWARE != 0
	}
	if idx, err := win.GetDisplayIndex(); err == nil {
		if mode, err := sdl.GetCurrentDisplayMode(idx); err == nil {
			s.info.RefreshHz = int(mode.RefreshRate)
		}
	}
	tex, err := loadGlyphAtlas(ren)
	if err != nil {
		ren.Destroy()
		win.Destroy()
		img.Quit()
		sdl.Quit()
		return nil, err
	}
	s.glyphTex = tex
	return s, nil
}

// ProcessEvents drains pending SDL window events; it reports true when the
// user asked to close the window (dev mode). Must run on the render thread.
// On KMSDRM there is no window manager, so this is a cheap no-op.
func (s *Screen) ProcessEvents() bool {
	for {
		switch ev := sdl.PollEvent(); e := ev.(type) {
		case nil:
			return false
		case *sdl.QuitEvent:
			_ = e
			return true
		}
	}
}

// SetMirror toggles horizontal flip at runtime (PATCH /config).
func (s *Screen) SetMirror(on bool) { s.mirror.Store(on) }

// Render decodes one JPEG (SDL2_image / libjpeg-turbo) and presents it
// full-screen, flipped when mirroring and letterboxed to the camera's aspect
// ratio (FR-16). Budget: decode 4–8 ms + present 2–4 ms at 720p on the Pi 5
// (validate in spike S-1).
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
	tex, err := s.frameTexture(surf)
	if err != nil {
		// Renderer-dependent: a driver may reject the surface's pixel
		// format for streaming textures. A one-shot upload keeps frames on
		// screen; the per-frame recreate count makes the fallback visible.
		oneShot, err := s.ren.CreateTextureFromSurface(surf)
		if err != nil {
			return fmt.Errorf("screen: texture: %w", err)
		}
		defer oneShot.Destroy()
		s.texRecreates.Add(1)
		tex = oneShot
	}

	flip := sdl.FLIP_NONE
	if s.mirror.Load() {
		flip = sdl.FLIP_HORIZONTAL
	}
	// Fit rather than fill: a stretched image is a lie to anyone checking
	// their own body line in the mirror (FR-16). A 4:3 camera on a 16:9 panel
	// pillarboxes instead of widening the dancer by a third.
	outW, outH, err := s.ren.GetOutputSize()
	if err != nil {
		return fmt.Errorf("screen: output size: %w", err)
	}
	x, y, w, h := fitRect(int(surf.W), int(surf.H), int(outW), int(outH))
	dst := &sdl.Rect{X: int32(x), Y: int32(y), W: int32(w), H: int32(h)}

	// Paint the bars black explicitly: Splash leaves its own backdrop colour
	// on the renderer, and the letterbox must not inherit it.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("screen: clear color: %w", err)
	}
	if err := s.ren.Clear(); err != nil {
		return fmt.Errorf("screen: clear: %w", err)
	}
	if err := s.ren.CopyEx(tex, nil, dst, 0, nil, flip); err != nil {
		return fmt.Errorf("screen: copy: %w", err)
	}
	if err := s.drawBadge(time.Duration(s.delayNS.Load())); err != nil {
		return err
	}
	s.ren.Present()
	return nil
}

// Splash paints a non-black backdrop plus the delay badge so the
// HDMI output shows something between SDL open and the first camera
// frame (typically 1–5 s of USB enumerate + first MJPEG decode). The
// render loop calls this only while warming up before any frame has
// been presented; after the first real Render the splash is never
// drawn again. Safe to call repeatedly — paints the same content,
// presents idempotently.
func (s *Screen) Splash() error {
	if err := s.ren.SetDrawColor(16, 18, 38, 255); err != nil {
		return fmt.Errorf("screen: splash color: %w", err)
	}
	if err := s.ren.Clear(); err != nil {
		return fmt.Errorf("screen: splash clear: %w", err)
	}
	if err := s.drawBadge(time.Duration(s.delayNS.Load())); err != nil {
		return err
	}
	s.ren.Present()
	return nil
}

// frameTexture returns the persistent streaming texture, updated with
// surf's pixels; it is (re)created only when the frame geometry changes.
func (s *Screen) frameTexture(surf *sdl.Surface) (*sdl.Texture, error) {
	if s.frameTex == nil || surf.W != s.texW || surf.H != s.texH || surf.Format.Format != s.texFmt {
		if s.frameTex != nil {
			s.frameTex.Destroy()
			s.frameTex = nil
		}
		tex, err := s.ren.CreateTexture(surf.Format.Format, sdl.TEXTUREACCESS_STREAMING, surf.W, surf.H)
		if err != nil {
			return nil, fmt.Errorf("screen: streaming texture: %w", err)
		}
		s.frameTex, s.texW, s.texH, s.texFmt = tex, surf.W, surf.H, surf.Format.Format
		s.texRecreates.Add(1)
	}
	if err := s.frameTex.Update(nil, surf.Data(), int(surf.Pitch)); err != nil {
		return nil, fmt.Errorf("screen: texture update: %w", err)
	}
	return s.frameTex, nil
}

// Close tears down SDL.
func (s *Screen) Close() error {
	if s.frameTex != nil {
		s.frameTex.Destroy()
	}
	if s.glyphTex != nil {
		s.glyphTex.Destroy()
	}
	s.ren.Destroy()
	s.win.Destroy()
	img.Quit()
	sdl.Quit()
	return nil
}
