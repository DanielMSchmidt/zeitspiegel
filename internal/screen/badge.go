//go:build sdl

package screen

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// Where to look for the badge typeface, best first. Liberation Sans is
// metrically identical to Arial and ships as fonts-liberation2; DejaVu is the
// fallback because it is on nearly every Debian, including one somebody
// installed by hand rather than from deploy/runtime-packages.txt.
//
// A missing font is not fatal: the bitmap atlas below still draws the badge.
// The lesson of libEGL is that a dlopened dependency should degrade loudly
// rather than take the display down with it, and deploy/check-runtime.sh
// reports its absence at bake, install and boot.
var badgeFontCandidates = []string{
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/System/Library/Fonts/Helvetica.ttc", // macOS, for make run-tv
}

//go:embed glyphs.png
var glyphsPNG []byte

// loadGlyphAtlas decodes the embedded PNG into an SDL texture.
func loadGlyphAtlas(ren *sdl.Renderer) (*sdl.Texture, error) {
	rw, err := sdl.RWFromMem(glyphsPNG)
	if err != nil {
		return nil, fmt.Errorf("screen: glyph rwops: %w", err)
	}
	surf, err := img.LoadRW(rw, true)
	if err != nil {
		return nil, fmt.Errorf("screen: decode glyph atlas: %w", err)
	}
	defer surf.Free()
	tex, err := ren.CreateTextureFromSurface(surf)
	if err != nil {
		return nil, fmt.Errorf("screen: glyph texture: %w", err)
	}
	return tex, nil
}

// openBadgeFont loads the first typeface it finds at px pixels. A nil font is
// a normal outcome, not an error: the caller falls back to the bitmap atlas.
func openBadgeFont(px int) (*ttf.Font, string) {
	if !ttf.WasInit() {
		if err := ttf.Init(); err != nil {
			return nil, ""
		}
	}
	for _, path := range badgeFontCandidates {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if f, err := ttf.OpenFont(path, px); err == nil {
			return f, path
		}
	}
	return nil, ""
}

// badgeText renders text into a texture at the current font size, caching it
// in the given slot: the delay changes when somebody moves the slider, not
// sixty times a second, and rasterising type on every frame would spend the
// render budget (NFR-2) on something that did not change.
//
// One slot per badge line rather than one cache overall. The two lines alternate
// within a single frame, so a single-entry cache would miss on every draw and
// re-rasterise both of them sixty times a second — the exact cost this exists
// to avoid.
func (s *Screen) badgeText(slot int, text string, px int) (*sdl.Texture, int32, int32, error) {
	if s.font == nil || s.fontPx != px {
		if s.font != nil {
			s.font.Close()
			s.font = nil
		}
		s.font, s.fontPath = openBadgeFont(px)
		s.fontPx = px
		s.dropBadgeCache()
		if s.font == nil {
			return nil, 0, 0, nil // atlas fallback
		}
	}
	if c := &s.badgeCache[slot]; c.tex != nil && c.str == text {
		return c.tex, c.w, c.h, nil
	}
	surf, err := s.font.RenderUTF8Blended(text, sdl.Color{R: 255, G: 255, B: 255, A: 255})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("screen: render badge text: %w", err)
	}
	defer surf.Free()
	tex, err := s.ren.CreateTextureFromSurface(surf)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("screen: badge text texture: %w", err)
	}
	s.dropBadgeSlot(slot)
	s.badgeCache[slot] = badgeCacheEntry{tex: tex, str: text, w: surf.W, h: surf.H}
	return tex, surf.W, surf.H, nil
}

func (s *Screen) dropBadgeSlot(slot int) {
	if c := &s.badgeCache[slot]; c.tex != nil {
		c.tex.Destroy()
		*c = badgeCacheEntry{}
	}
}

func (s *Screen) dropBadgeCache() {
	for i := range s.badgeCache {
		s.dropBadgeSlot(i)
	}
}

// drawOverlay renders everything that sits on top of the frame: the delay
// badge, and under it the warm-up countdown when the mirror cannot yet reach
// as far back as the delay asks (FR-10, FR-13). Must be called after CopyEx
// so neither is mirror-flipped.
//
// The countdown is what stops a warming mirror from reading as a crashed one:
// during warm-up the selected frame is pinned to the oldest one buffered, so
// the picture is a still image for up to `delay` seconds, and a still image
// with no explanation on it is what a field test reported as a freeze.
func (s *Screen) drawOverlay() error {
	badge, err := s.drawLine(badgeSlotDelay, formatDelay(time.Duration(s.delayNS.Load())), nil)
	if err != nil {
		return err
	}
	warm := formatWarmup(time.Duration(s.warmupNS.Load()))
	if warm == "" {
		return nil
	}
	_, err = s.drawLine(badgeSlotWarmup, warm, &badge)
	return err
}

// drawLine renders one badge line in the top-right corner, stacked under
// `under` when one is given. It returns where it drew, so the next line can
// be placed below it.
func (s *Screen) drawLine(slot int, text string, under *badgeRect) (badgeRect, error) {
	winW, winH, err := s.ren.GetOutputSize()
	if err != nil {
		return badgeRect{}, fmt.Errorf("screen: badge output size: %w", err)
	}
	// Sized against the screen it is actually on (badgelayout.go), not against
	// the monitor this was first written for.
	tex, textW, textH, err := s.badgeText(slot, text, badgeFontPx(int(winH)))
	if err != nil {
		return badgeRect{}, err
	}
	var l badgeRect
	if tex != nil {
		l = badgeLayoutText(int(winW), int(winH), int(textW), int(textH))
	} else {
		l = badgeLayout(int(winW), int(winH), len([]rune(text)))
	}
	if under != nil {
		l = stackUnder(*under, l)
	}
	bx, by := int32(l.X), int32(l.Y)
	badgeW, badgeH := int32(l.W), int32(l.H)

	// Opaque black backdrop.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return badgeRect{}, fmt.Errorf("screen: badge color: %w", err)
	}
	if err := s.ren.FillRect(&sdl.Rect{X: bx, Y: by, W: badgeW, H: badgeH}); err != nil {
		return badgeRect{}, fmt.Errorf("screen: badge rect: %w", err)
	}

	// One blit of real type when a font was found.
	if tex != nil {
		dst := sdl.Rect{X: bx + int32(l.PadInner), Y: by + int32(l.PadInner), W: textW, H: textH}
		if err := s.ren.Copy(tex, nil, &dst); err != nil {
			return badgeRect{}, fmt.Errorf("screen: badge text: %w", err)
		}
		return l, nil
	}

	// Otherwise the bitmap atlas, glyph by glyph.
	for i, r := range text {
		rawIdx := strings.IndexRune(atlasOrder, r)
		if rawIdx < 0 {
			return badgeRect{}, fmt.Errorf("screen: badge has unknown rune %q", r)
		}
		idx := int32(rawIdx)
		src := sdl.Rect{X: idx * glyphW, Y: 0, W: glyphW, H: glyphH}
		dst := sdl.Rect{
			X: bx + int32(l.PadInner) + int32(i)*int32(l.GlyphW),
			Y: by + int32(l.PadInner),
			W: int32(l.GlyphW),
			H: int32(l.GlyphH),
		}
		if err := s.ren.Copy(s.glyphTex, &src, &dst); err != nil {
			return badgeRect{}, fmt.Errorf("screen: badge glyph: %w", err)
		}
	}
	return l, nil
}
