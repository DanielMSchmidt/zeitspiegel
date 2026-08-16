package screen

import (
	"strings"
	"testing"
	"time"
)

// UT-11
func TestFormatDelay(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "0s delay"},
		{"two seconds", 2 * time.Second, "2s delay"},
		{"thirty seconds", 30 * time.Second, "30s delay"},
		{"ninety seconds", 90 * time.Second, "90s delay"},
		{"sub-second rounds down", 999 * time.Millisecond, "0s delay"},
		{"negative clamps to zero", -5 * time.Second, "0s delay"},
		{"runaway clamps to 9999s", 4 * time.Hour, "9999s delay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDelay(tc.in)
			if got != tc.want {
				t.Fatalf("formatDelay(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// UT-45: the warm-up line under the badge. A mirror whose delay reaches
// further back than it has recorded shows a still frame (FR-10); this is the
// text that says so, and says for how long, so a frozen picture reads as a
// countdown rather than as a crash.
func TestFormatWarmup(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"twelve seconds", 12 * time.Second, "ready in 12s"},
		{"one second", time.Second, "ready in 1s"},
		// Rounds up, never down: a countdown reading "0s" while the picture is
		// still frozen is the exact lie this line exists to stop telling.
		{"part second rounds up", 200 * time.Millisecond, "ready in 1s"},
		{"just under two seconds", 1999 * time.Millisecond, "ready in 2s"},
		{"zero means done", 0, ""},
		{"negative means done", -3 * time.Second, ""},
		{"runaway clamps to 9999s", 4 * time.Hour, "ready in 9999s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatWarmup(tc.in); got != tc.want {
				t.Fatalf("formatWarmup(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// UT-45: every rune the badge can put on screen has a cell in the atlas.
// The bitmap fallback errors on an unknown rune — on a unit with no typeface
// installed that would take the whole render down — and the atlas is drawn by
// a generator holding its own copy of the order string, so nothing else
// notices when the two drift apart.
func TestBadgeRunesAreInTheAtlas(t *testing.T) {
	texts := []string{
		formatDelay(0), formatDelay(9999 * time.Second),
		formatWarmup(time.Second), formatWarmup(9999 * time.Second),
	}
	for _, s := range texts {
		for _, r := range s {
			if !strings.ContainsRune(atlasOrder, r) {
				t.Errorf("rune %q in %q has no atlas cell (atlasOrder = %q)", r, s, atlasOrder)
			}
		}
	}
}
