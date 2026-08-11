package camera

import "testing"

// UT-28
func TestSelectMode(t *testing.T) {
	const target = 30.0
	cases := []struct {
		name  string
		modes []mode
		want  mode
	}{
		{
			// The bug this replaces: area alone picked 1600x1200@15, and the
			// pipeline then asked the device for 30 fps it could not deliver.
			"trades resolution to keep the frame rate",
			[]mode{{1600, 1200, 15}, {1280, 720, 30}},
			mode{1280, 720, 30},
		},
		{
			// E-2: spatial sharpness wins once the rate target is met.
			"prefers area over extra frame rate",
			[]mode{{1920, 1080, 30}, {1280, 720, 60}},
			mode{1920, 1080, 30},
		},
		{
			"same area, faster wins",
			[]mode{{1920, 1080, 30}, {1920, 1080, 60}},
			mode{1920, 1080, 60},
		},
		{
			// 30000/1001 — a strict >= 30 would wrongly demote this to tier B.
			"29.97 counts as reaching 30",
			[]mode{{1920, 1080, 30000.0 / 1001.0}, {1280, 720, 30}},
			mode{1920, 1080, 30000.0 / 1001.0},
		},
		{
			"modes above the cap are filtered out",
			[]mode{{3840, 2160, 30}, {2560, 1440, 30}, {1920, 1080, 30}},
			mode{1920, 1080, 30},
		},
		{
			"a too-wide but short mode is still filtered",
			[]mode{{2048, 1080, 60}, {1280, 720, 30}},
			mode{1280, 720, 30},
		},
		{
			// Tier B: never fail, take the closest rate available.
			"nothing reaches the target, fastest wins",
			[]mode{{1920, 1080, 15}, {640, 480, 25}},
			mode{640, 480, 25},
		},
		{
			"tier B ties on rate, largest wins",
			[]mode{{640, 480, 20}, {1280, 720, 20}},
			mode{1280, 720, 20},
		},
		{
			"only 60 is offered, take it",
			[]mode{{1920, 1080, 60}},
			mode{1920, 1080, 60},
		},
		{
			// A device that enumerates sizes but not intervals must still open.
			"unknown rate is still selectable",
			[]mode{{1920, 1080, 0}},
			mode{1920, 1080, 0},
		},
		{
			"a known-good rate beats an unknown one",
			[]mode{{1920, 1080, 0}, {1280, 720, 30}},
			mode{1280, 720, 30},
		},
		{
			"single qualifying mode",
			[]mode{{1280, 720, 30}},
			mode{1280, 720, 30},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectMode(tc.modes, 1920, 1080, target)
			if err != nil {
				t.Fatalf("selectMode(%v) error = %v, want %+v", tc.modes, err, tc.want)
			}
			if got != tc.want {
				t.Fatalf("selectMode(%v) = %+v, want %+v", tc.modes, got, tc.want)
			}
		})
	}
}

// UT-28: only an empty candidate set is fatal — a camera with no MJPEG mode at
// or below the cap genuinely cannot be used.
func TestSelectModeNoCandidates(t *testing.T) {
	cases := []struct {
		name  string
		modes []mode
	}{
		{"empty", nil},
		{"everything above the cap", []mode{{3840, 2160, 30}, {2560, 1440, 60}}},
		{"degenerate dimensions", []mode{{0, 0, 30}, {1920, 0, 30}, {0, 1080, 30}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := selectMode(tc.modes, 1920, 1080, 30); err == nil {
				t.Fatalf("selectMode(%v) = %+v, want an error", tc.modes, got)
			}
		})
	}
}

// UT-28: selection must not depend on enumeration order — the same camera has
// to come up the same way on every boot.
func TestSelectModeStableAcrossOrdering(t *testing.T) {
	modes := []mode{{1280, 720, 30}, {1920, 1080, 15}, {1920, 1080, 30}, {640, 480, 60}}
	want, err := selectMode(modes, 1920, 1080, 30)
	if err != nil {
		t.Fatalf("selectMode: %v", err)
	}
	if want != (mode{1920, 1080, 30}) {
		t.Fatalf("selectMode = %+v, want 1920x1080@30", want)
	}
	// Every rotation of the same set must agree.
	for i := range modes {
		rotated := append(append([]mode{}, modes[i:]...), modes[:i]...)
		got, err := selectMode(rotated, 1920, 1080, 30)
		if err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("rotation %d: selectMode = %+v, want %+v", i, got, want)
		}
	}
}
