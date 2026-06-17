package screen

import (
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
