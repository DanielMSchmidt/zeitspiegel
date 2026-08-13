//go:build sdl

package screen

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

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
	l := badgeLayout(int(winW), int(winH), len([]rune(text)))
	bx, by := int32(l.X), int32(l.Y)
	badgeW, badgeH := int32(l.W), int32(l.H)

	// Opaque black backdrop.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("screen: badge color: %w", err)
	}
	if err := s.ren.FillRect(&sdl.Rect{X: bx, Y: by, W: badgeW, H: badgeH}); err != nil {
		return fmt.Errorf("screen: badge rect: %w", err)
	}

	// Blit each glyph from the atlas.
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
