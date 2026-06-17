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
