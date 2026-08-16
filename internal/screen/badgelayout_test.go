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

// UT-40: with a real font the size is no longer tied to a bitmap cell, so the
// type is exactly the fraction of the screen it should be — 1440p stops
// sharing 1080p's size because the atlas happened to double there.
func TestBadgeFontPx(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    int
		want int
	}{
		{"720p", 720, 28},
		{"1080p", 1080, 43},
		{"1440p", 1440, 57},
		{"4K", 2160, 86},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := badgeFontPx(tc.h); got != tc.want {
				t.Errorf("badgeFontPx(%d) = %d, want %d", tc.h, got, tc.want)
			}
		})
	}
	// A tiny or nonsensical output still yields type somebody could read,
	// never zero or negative — SDL refuses to open a font at 0 px.
	for _, h := range []int{0, -1, 8, 120} {
		if px := badgeFontPx(h); px < 12 {
			t.Errorf("badgeFontPx(%d) = %d, too small to render", h, px)
		}
	}
}

// UT-40: with a measured string the box is built around what the font
// actually drew, rather than around a count of fixed-width cells.
func TestBadgeLayoutTextWrapsMeasuredText(t *testing.T) {
	const w, h = 1920, 1080
	l := badgeLayoutText(w, h, 200, 43)
	if l.W <= 200 || l.H <= 43 {
		t.Errorf("box %dx%d does not contain 200x43 of text plus padding", l.W, l.H)
	}
	if l.X+l.W >= w || l.Y <= 0 || l.X < 0 {
		t.Errorf("box %+v is not inset in the top-right of %dx%d", l, w, h)
	}
	// Padding scales with the type, so the box never looks tight at 4K or
	// bloated at 720p.
	small := badgeLayoutText(1280, 720, 120, 28)
	if small.PadInner >= l.PadInner {
		t.Errorf("padding did not grow with the type: %d at 720p vs %d at 1080p", small.PadInner, l.PadInner)
	}
	// Text wider than the screen still starts on it.
	wide := badgeLayoutText(640, 480, 5000, 30)
	if wide.X < 0 || wide.Y < 0 {
		t.Errorf("oversized text pushed the box off-screen: %+v", wide)
	}
}

// UT-45: the warm-up line sits directly under the delay badge, right-aligned
// to the same edge. Two boxes drawn independently in the same corner would
// overlap, and a mirror that is already showing a still frame is the worst
// place to stack unreadable text.
func TestBadgeStackUnderKeepsTheLinesApart(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"720p", 1280, 720},
		{"1080p", 1920, 1080},
		{"4K", 3840, 2160},
		{"degenerate", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := badgeLayout(tc.w, tc.h, 8)   // "25s delay"
			second := badgeLayout(tc.w, tc.h, 12) // "ready in 12s" — wider
			stacked := stackUnder(first, second)

			if stacked.Y < first.Y+first.H {
				t.Errorf("second line at y=%d overlaps the badge ending at y=%d", stacked.Y, first.Y+first.H)
			}
			// Right-aligned to the badge's own edge, so the two read as one
			// block rather than as two things that happen to be near each
			// other. A line too wide to sit there is pushed flush left rather
			// than off the screen — the degenerate case, where the renderer
			// reported a size nothing fits in.
			if stacked.X < 0 {
				t.Errorf("second line starts off-screen at x=%d", stacked.X)
			}
			if got, want := stacked.X+stacked.W, first.X+first.W; got != want && stacked.X != 0 {
				t.Errorf("second line right edge %d, badge right edge %d", got, want)
			}
			// Everything else about the line is untouched.
			if stacked.W != second.W || stacked.H != second.H || stacked.GlyphW != second.GlyphW {
				t.Errorf("stackUnder resized the line: %+v, want the size of %+v", stacked, second)
			}
			if tc.w > 0 && tc.h > 0 && (stacked.X < 0 || stacked.Y+stacked.H > tc.h) {
				t.Errorf("second line %+v escapes a %dx%d screen", stacked, tc.w, tc.h)
			}
		})
	}
}
