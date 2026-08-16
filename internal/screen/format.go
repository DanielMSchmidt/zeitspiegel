package screen

import (
	"fmt"
	"time"
)

// formatDelay renders d as "Ns delay" for the on-screen badge. Negatives
// clamp to zero; anything above 9999 s clamps to 9999 s, just so the badge
// width cannot grow without bound if a runaway delay is ever set.
func formatDelay(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d / time.Second)
	if sec > 9999 {
		sec = 9999
	}
	return fmt.Sprintf("%ds delay", sec)
}

// formatWarmup renders the second badge line: how long until the mirror can
// actually reach back as far as the delay asks (FR-10). Empty when there is
// nothing left to wait for, which is what tells the renderer to draw no line
// at all.
//
// Seconds round up rather than down. During warm-up the picture is a still
// frame, and a countdown that reaches "0s" while it is still frozen would
// leave the viewer exactly where they started — looking at a stopped mirror
// with nothing on screen admitting it.
//
// Every rune here must have a cell in the glyph atlas (UT-45): a unit with no
// typeface installed draws this from the bitmap fallback, which fails on a
// rune it does not know.
func formatWarmup(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	sec := int((d + time.Second - 1) / time.Second)
	if sec > 9999 {
		sec = 9999
	}
	return fmt.Sprintf("ready in %ds", sec)
}
