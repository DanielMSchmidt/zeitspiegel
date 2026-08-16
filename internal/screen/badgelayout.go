package screen

// Atlas geometry — must match internal/screen/fontgen/main.go.
const (
	// Appended to, never reordered: a cell's index is its position here, so
	// moving one would silently repaint every badge with the wrong glyphs.
	// UT-45 asserts the badge never emits a rune this string lacks.
	atlasOrder = "0123456789s delayrin"
	glyphW     = 14
	glyphH     = 24

	// Badge padding, expressed in atlas cells rather than pixels so it grows
	// with the type: 1/6 of a cell inside the box, 2/3 of one from the screen
	// edge. At scale 1 these are the 4 px and 16 px the badge used when it was
	// laid out for one particular monitor.
	badgePadInnerNum, badgePadInnerDen = 1, 6
	badgePadEdgeNum, badgePadEdgeDen   = 2, 3

	// Target text height as a fraction of screen height. A mirror is watched
	// from across a room, so the delay has to be legible from there; it is
	// also not the thing being watched, so it stays out of the way. Four per
	// cent puts it at roughly a fingertip's width on a studio TV.
	badgeHeightNum, badgeHeightDen = 1, 25

	// Never smaller than this, whatever the renderer reports: SDL will not
	// open a font at zero pixels, and type nobody can read is no better.
	badgeMinPx = 16
)

// badgeFontPx is the pixel height for the badge's type on a screenH-tall
// output. With a real font there is no atlas cell to round to, so this is the
// exact fraction — which is why 1440p no longer inherits 1080p's size just
// because doubling a bitmap happened to land there.
//
// The floor matters: SDL refuses to open a font at zero pixels, and a screen
// the renderer describes as tiny (or nonsensically) still has to show
// something a person could read.
func badgeFontPx(screenH int) int {
	px := screenH * badgeHeightNum / badgeHeightDen
	if px < badgeMinPx {
		px = badgeMinPx
	}
	return px
}

// badgeLayoutText places the badge around text a font has already measured.
// The bitmap path counts fixed-width cells; a real font does not have any, so
// the box is built around what was actually drawn.
func badgeLayoutText(screenW, screenH, textW, textH int) badgeRect {
	if textH < 1 {
		textH = badgeMinPx
	}
	padInner := textH * badgePadInnerNum / badgePadInnerDen
	padEdge := textH * badgePadEdgeNum / badgePadEdgeDen
	b := badgeRect{
		W:        textW + 2*padInner,
		H:        textH + 2*padInner,
		GlyphW:   textW,
		GlyphH:   textH,
		PadInner: padInner,
	}
	b.X = screenW - padEdge - b.W
	b.Y = padEdge
	if b.X < 0 {
		b.X = 0
	}
	if b.Y < 0 {
		b.Y = 0
	}
	return b
}

// stackUnder moves line directly below above, right-aligned to the same edge,
// leaving one inner padding between them. Both boxes are laid out for the
// top-right corner on their own, so without this the warm-up line would be
// drawn on top of the delay badge.
//
// The gap comes from the badge's own padding, so the block breathes the same
// amount at 720p as at 4K.
func stackUnder(above, line badgeRect) badgeRect {
	line.Y = above.Y + above.H + above.PadInner
	line.X = above.X + above.W - line.W
	if line.X < 0 {
		line.X = 0
	}
	return line
}

// badgeRect is where the delay badge goes and how big its type is, in pixels.
type badgeRect struct {
	X, Y, W, H     int // the black box
	GlyphW, GlyphH int // one glyph, scaled from the atlas cell
	PadInner       int // between the type and the box edge
}

// badgeLayout places the badge in the top-right corner of a screenW×screenH
// output for a string of n glyphs.
//
// The size is a fraction of the screen, not a pixel count: the badge used to
// be 24 px tall whatever it was shown on, which is legible on the desk monitor
// it was written against and a postage stamp on the studio TV it ends up
// facing.
//
// The scale is a whole multiple of the atlas cell. A bitmap font blown up by a
// fractional factor is resampled onto half-pixels and comes out smeared, and
// smeared type at a distance is worse than slightly smaller type.
func badgeLayout(screenW, screenH, glyphs int) badgeRect {
	if glyphs < 1 {
		glyphs = 1
	}
	scale := 1
	if screenW > 0 && screenH > 0 {
		target := screenH * badgeHeightNum / badgeHeightDen
		// Round to nearest rather than truncate: at 1080p the exact figure is
		// 1.8 cells, and rounding down would leave the badge at its old size
		// on the most common panel there is.
		scale = (target + glyphH/2) / glyphH
		if scale < 1 {
			scale = 1
		}
	}

	gw, gh := glyphW*scale, glyphH*scale
	padInner := gh * badgePadInnerNum / badgePadInnerDen
	padEdge := gh * badgePadEdgeNum / badgePadEdgeDen

	b := badgeRect{
		W:        glyphs*gw + 2*padInner,
		H:        gh + 2*padInner,
		GlyphW:   gw,
		GlyphH:   gh,
		PadInner: padInner,
	}
	b.X = screenW - padEdge - b.W
	b.Y = padEdge
	// A screen too small to hold the badge, or one the renderer reported as
	// degenerate, still gets a rectangle that is on the screen rather than a
	// negative one that SDL would refuse.
	if b.X < 0 {
		b.X = 0
	}
	if b.Y < 0 {
		b.Y = 0
	}
	return b
}
