//go:build sdl

package screen

import (
	_ "embed"
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

//go:embed glyphs.png
var glyphsPNG []byte

// Atlas geometry — must match internal/screen/fontgen/main.go.
const (
	atlasOrder    = "0123456789:"
	glyphW        = 14
	glyphH        = 24
	badgePadEdge  = 16 // px from top + right edges of the screen
	badgePadInner = 4  // px between text and badge border, each side
)

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

// drawBadge renders the 5-char MM:SS badge in the top-right corner.
// Must be called after CopyEx so it is never mirror-flipped.
func (s *Screen) drawBadge(d time.Duration) error {
	text := formatDelay(d)
	if len(text) != 5 {
		return fmt.Errorf("screen: badge text %q not 5 chars", text)
	}
	winW, _ := s.win.GetSize()
	innerW := int32(len(text)) * glyphW
	badgeW := innerW + 2*badgePadInner
	badgeH := int32(glyphH) + 2*badgePadInner
	bx := winW - badgePadEdge - badgeW
	by := int32(badgePadEdge)

	// Opaque black backdrop.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("screen: badge color: %w", err)
	}
	if err := s.ren.FillRect(&sdl.Rect{X: bx, Y: by, W: badgeW, H: badgeH}); err != nil {
		return fmt.Errorf("screen: badge rect: %w", err)
	}

	// Blit each glyph from the atlas.
	for i, r := range text {
		idx := int32(indexOf(atlasOrder, r))
		if idx < 0 {
			return fmt.Errorf("screen: badge has unknown rune %q", r)
		}
		src := sdl.Rect{X: idx * glyphW, Y: 0, W: glyphW, H: glyphH}
		dst := sdl.Rect{
			X: bx + badgePadInner + int32(i)*glyphW,
			Y: by + badgePadInner,
			W: glyphW,
			H: glyphH,
		}
		if err := s.ren.Copy(s.glyphTex, &src, &dst); err != nil {
			return fmt.Errorf("screen: badge glyph: %w", err)
		}
	}
	return nil
}

func indexOf(s string, r rune) int {
	for i, c := range s {
		if c == r {
			return i
		}
	}
	return -1
}
