//go:build sdl

package screen

// The badge's other text path. A unit with no typeface installed draws every
// line from the embedded bitmap atlas, which fails on any rune it has no cell
// for — and both the CI machine and the baked card have fonts, so nothing
// else ever walks this branch. The tag-free UT-45 rune check guards the same
// thing statically; this one proves the draw itself survives.

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func openAtlasOnly(t *testing.T) *Screen {
	t.Helper()
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	// No candidate typefaces ⇒ openBadgeFont finds nothing ⇒ atlas fallback.
	saved := badgeFontCandidates
	badgeFontCandidates = nil
	t.Cleanup(func() { badgeFontCandidates = saved })

	s, err := Open(Options{Mirror: true, Windowed: true, Width: 320, Height: 240})
	if err != nil {
		t.Fatalf("Open with dummy driver: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// UT-45: every badge line the mirror can show draws from the atlas alone.
func TestBadgeLinesDrawFromTheAtlas(t *testing.T) {
	s := openAtlasOnly(t)
	for _, tc := range []struct{ delay, warm time.Duration }{
		{0, 0},
		{25 * time.Second, 12 * time.Second},
		{90 * time.Second, time.Second},
		{4 * time.Hour, 4 * time.Hour}, // both clamp paths
		{-1 * time.Second, -1 * time.Second},
	} {
		s.SetDelay(tc.delay)
		s.SetWarmup(tc.warm)
		if err := s.Splash(); err != nil {
			t.Fatalf("delay %v, warm-up %v: %v", tc.delay, tc.warm, err)
		}
	}
	// The typeface is opened lazily on the first draw, so this is only
	// meaningful after one: prove the run above really took the atlas branch
	// rather than quietly finding a font.
	if s.font != nil {
		t.Fatal("a typeface was opened despite an empty candidate list")
	}
}

// UT-45: the two lines cache their type separately. They alternate within a
// single frame, so one shared slot would miss on every draw and re-rasterise
// both of them on every tick — the cost the cache exists to avoid.
func TestBadgeLinesCacheSeparately(t *testing.T) {
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	s, err := Open(Options{Windowed: true, Width: 320, Height: 240})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	s.SetDelay(25 * time.Second)
	s.SetWarmup(12 * time.Second)
	if err := s.Splash(); err != nil {
		t.Fatalf("splash: %v", err)
	}
	// The typeface is opened on the first draw, not in Open.
	if s.font == nil {
		t.Skip("no typeface on this machine; the atlas path caches nothing")
	}
	delayTex := s.badgeCache[badgeSlotDelay].tex
	warmTex := s.badgeCache[badgeSlotWarmup].tex
	if delayTex == nil || warmTex == nil {
		t.Fatal("a badge line was drawn without caching its texture")
	}
	if delayTex == warmTex {
		t.Fatal("both lines share one cached texture")
	}

	// Same text again: neither is re-rasterised.
	if err := s.Splash(); err != nil {
		t.Fatalf("second splash: %v", err)
	}
	if s.badgeCache[badgeSlotDelay].tex != delayTex || s.badgeCache[badgeSlotWarmup].tex != warmTex {
		t.Error("unchanged badge text was rasterised again")
	}

	// Only the countdown moves: the delay line keeps its texture.
	s.SetWarmup(11 * time.Second)
	if err := s.Splash(); err != nil {
		t.Fatalf("third splash: %v", err)
	}
	if s.badgeCache[badgeSlotDelay].tex != delayTex {
		t.Error("the delay line was rasterised again when only the countdown changed")
	}
	if s.badgeCache[badgeSlotWarmup].str != formatWarmup(11*time.Second) {
		t.Errorf("countdown cache holds %q, want %q",
			s.badgeCache[badgeSlotWarmup].str, formatWarmup(11*time.Second))
	}
}

var _ = sdl.INIT_VIDEO // this file is meaningless without the sdl bindings
