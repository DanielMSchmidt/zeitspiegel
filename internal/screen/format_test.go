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
		{"zero", 0, "00:00"},
		{"thirty seconds", 30 * time.Second, "00:30"},
		{"one minute thirty", 90 * time.Second, "01:30"},
		{"sixty-one minutes", 61 * time.Minute, "61:00"},
		{"two hours clamps to max", 2 * time.Hour, "99:59"},
		{"negative clamps to zero", -5 * time.Second, "00:00"},
		{"sub-second rounds down", 999 * time.Millisecond, "00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDelay(tc.in)
			if got != tc.want {
				t.Fatalf("formatDelay(%v) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) != 5 {
				t.Fatalf("formatDelay(%v) = %q, want fixed-width 5 chars", tc.in, got)
			}
		})
	}
}
