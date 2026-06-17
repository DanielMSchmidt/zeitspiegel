package screen

import (
	"fmt"
	"time"
)

// formatDelay renders d as fixed-width "MM:SS" for the on-screen badge.
// Clamps negatives to zero and anything above 99:59 to 99:59 — the buffer
// can never reach that duration in practice, but the display must stay
// five glyphs wide regardless of input.
func formatDelay(d time.Duration) string {
	const max = 99*time.Minute + 59*time.Second
	if d < 0 {
		d = 0
	}
	if d > max {
		d = max
	}
	totalSec := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", totalSec/60, totalSec%60)
}
