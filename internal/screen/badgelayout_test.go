package screen

import "testing"

// UT-40: the badge was laid out in fixed pixels, so it was sized for whatever
// panel it was first seen on — legible on a monitor at arm's length,
// a postage stamp on a studio TV, and invisible on a 4K one. Its size is a
// fraction of the screen, not a number of pixels.
func TestBadgeLayoutScalesWithTheScreen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		w, h    int
		wantMin int // text height, px
		wantMax int
	}{
		// Roughly 4 % of the height: readable across a studio, nowhere near
		// dominating the mirror the dancer is actually looking at.
		{"720p", 1280, 720, 24, 36},
		{"1080p", 1920, 1080, 36, 52},
		{"1440p", 2560, 1440, 48, 68},
		{"4K", 3840, 2160, 72, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := badgeLayout(tc.w, tc.h, 5) // "25 s" — five glyphs
			if l.GlyphH < tc.wantMin || l.GlyphH > tc.wantMax {
				t.Errorf("glyph height %d px on a %dp screen, want %d–%d", l.GlyphH, tc.h, tc.wantMin, tc.wantMax)
			}
			// Proportions come from the atlas cell, so the type never stretches.
			if got, want := float64(l.GlyphW)/float64(l.GlyphH), float64(glyphW)/float64(glyphH); got < want*0.99 || got > want*1.01 {
				t.Errorf("glyph aspect %.3f, want %.3f — the type is being stretched", got, want)
			}
			// The badge stays inside the screen, with its padding.
			if l.X < 0 || l.Y < 0 || l.X+l.W > tc.w || l.Y+l.H > tc.h {
				t.Errorf("badge %+v escapes a %dx%d screen", l, tc.w, tc.h)
			}
			// Top-right corner, inset — not touching either edge.
			if l.X+l.W >= tc.w || l.Y <= 0 {
				t.Errorf("badge %+v is not inset from the top-right corner", l)
			}
		})
	}
}

// UT-40: integer multiples of the atlas cell keep a bitmap font crisp; a
// fractional scale is what makes blown-up pixel type look smeared.
func TestBadgeLayoutScalesTheAtlasByWholePixels(t *testing.T) {
	for _, h := range []int{720, 768, 900, 1080, 1200, 1440, 2160} {
		l := badgeLayout(h*16/9, h, 5)
		if l.GlyphH%glyphH != 0 || l.GlyphW%glyphW != 0 {
			t.Errorf("screen height %d: glyph %dx%d is not a whole multiple of the %dx%d cell",
				h, l.GlyphW, l.GlyphH, glyphW, glyphH)
		}
	}
}

// UT-40: a screen the renderer reports as degenerate must not produce a
// negative rectangle or a division by zero — it falls back to the atlas's own
// size, which is what the badge did before it scaled at all.
func TestBadgeLayoutSurvivesADegenerateScreen(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{0, 0}, {-1, 720}, {1280, 0}} {
		l := badgeLayout(tc.w, tc.h, 5)
		if l.GlyphH != glyphH || l.GlyphW != glyphW {
			t.Errorf("%dx%d: glyph %dx%d, want the unscaled %dx%d", tc.w, tc.h, l.GlyphW, l.GlyphH, glyphW, glyphH)
		}
		if l.W <= 0 || l.H <= 0 {
			t.Errorf("%dx%d: badge has non-positive size %+v", tc.w, tc.h, l)
		}
	}
}
