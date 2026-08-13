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

// badgeText renders text into a texture at the current font size, caching it:
// the delay changes when somebody moves the slider, not sixty times a second,
// and rasterising type on every frame would spend the render budget (NFR-2) on
// something that did not change.
func (s *Screen) badgeText(text string, px int) (*sdl.Texture, int32, int32, error) {
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
	if s.badgeTex != nil && s.badgeStr == text {
		return s.badgeTex, s.badgeW, s.badgeH, nil
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
	s.dropBadgeCache()
	s.badgeTex, s.badgeStr = tex, text
	s.badgeW, s.badgeH = surf.W, surf.H
	return tex, surf.W, surf.H, nil
}

func (s *Screen) dropBadgeCache() {
	if s.badgeTex != nil {
		s.badgeTex.Destroy()
		s.badgeTex, s.badgeStr = nil, ""
	}
}

// drawBadge renders the "Ns delay" badge in the top-right corner. Must
// be called after CopyEx so it is never mirror-flipped.
func (s *Screen) drawBadge(d time.Duration) error {
	text := formatDelay(d)
	winW, winH, err := s.ren.GetOutputSize()
	if err != nil {
		return fmt.Errorf("screen: badge output size: %w", err)
	}
	// Sized against the screen it is actually on (badgelayout.go), not against
	// the monitor this was first written for.
	tex, textW, textH, err := s.badgeText(text, badgeFontPx(int(winH)))
	if err != nil {
		return err
	}
	var l badgeRect
	if tex != nil {
		l = badgeLayoutText(int(winW), int(winH), int(textW), int(textH))
	} else {
		l = badgeLayout(int(winW), int(winH), len([]rune(text)))
	}
	bx, by := int32(l.X), int32(l.Y)
	badgeW, badgeH := int32(l.W), int32(l.H)

	// Opaque black backdrop.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("screen: badge color: %w", err)
	}
	if err := s.ren.FillRect(&sdl.Rect{X: bx, Y: by, W: badgeW, H: badgeH}); err != nil {
		return fmt.Errorf("screen: badge rect: %w", err)
	}

	// One blit of real type when a font was found.
	if tex != nil {
		dst := sdl.Rect{X: bx + int32(l.PadInner), Y: by + int32(l.PadInner), W: textW, H: textH}
		if err := s.ren.Copy(tex, nil, &dst); err != nil {
			return fmt.Errorf("screen: badge text: %w", err)
		}
		return nil
	}

	// Otherwise the bitmap atlas, glyph by glyph.
	for i, r := range text {
		rawIdx := strings.IndexRune(atlasOrder, r)
		if rawIdx < 0 {
			return fmt.Errorf("screen: badge has unknown rune %q", r)
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
			return fmt.Errorf("screen: badge glyph: %w", err)
		}
	}
	return nil
}
