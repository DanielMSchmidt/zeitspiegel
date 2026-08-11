package screen

import "testing"

// UT-27
func TestFitRect(t *testing.T) {
	cases := []struct {
		name                   string
		srcW, srcH, dstW, dstH int
		x, y, w, h             int
	}{
		{"identical geometry fills", 1920, 1080, 1920, 1080, 0, 0, 1920, 1080},
		{"same aspect scales up", 1280, 720, 1920, 1080, 0, 0, 1920, 1080},
		{"same aspect scales down", 1920, 1080, 1280, 720, 0, 0, 1280, 720},
		// The case that motivates this: probeMaxMJPEG prefers 1600x1200 over
		// 1280x720 (larger area), and stretching that to a 16:9 panel widens
		// the dancer by a third.
		{"4:3 into 16:9 pillarboxes", 1600, 1200, 1920, 1080, 240, 0, 1440, 1080},
		{"16:9 into 4:3 letterboxes", 1920, 1080, 1600, 1200, 0, 150, 1600, 900},
		{"square into 16:9 pillarboxes", 1000, 1000, 1920, 1080, 420, 0, 1080, 1080},
		{"degenerate 1x1 source", 1, 1, 1920, 1080, 420, 0, 1080, 1080},
		// Never divide by zero and never blank the screen: fall back to the
		// old fill-everything behaviour.
		{"zero source width fills", 0, 1080, 1920, 1080, 0, 0, 1920, 1080},
		{"zero source height fills", 1920, 0, 1920, 1080, 0, 0, 1920, 1080},
		{"negative source fills", -16, -9, 1920, 1080, 0, 0, 1920, 1080},
		{"zero destination fills", 1920, 1080, 0, 0, 0, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y, w, h := fitRect(tc.srcW, tc.srcH, tc.dstW, tc.dstH)
			if x != tc.x || y != tc.y || w != tc.w || h != tc.h {
				t.Fatalf("fitRect(%d, %d, %d, %d) = (%d, %d, %d, %d), want (%d, %d, %d, %d)",
					tc.srcW, tc.srcH, tc.dstW, tc.dstH, x, y, w, h, tc.x, tc.y, tc.w, tc.h)
			}
		})
	}
}

// UT-27: the fitted rect never leaves the destination and never inverts the
// source aspect, for any geometry the probe could hand us.
func TestFitRectStaysInsideDestination(t *testing.T) {
	sizes := []int{1, 3, 7, 16, 240, 480, 720, 1080, 1200, 1920, 2160, 3840}
	for _, srcW := range sizes {
		for _, srcH := range sizes {
			for _, dst := range [][2]int{{1920, 1080}, {1280, 720}, {1024, 768}, {800, 1280}} {
				x, y, w, h := fitRect(srcW, srcH, dst[0], dst[1])
				if x < 0 || y < 0 || w < 0 || h < 0 || x+w > dst[0] || y+h > dst[1] {
					t.Fatalf("fitRect(%d, %d, %d, %d) = (%d, %d, %d, %d) escapes destination",
						srcW, srcH, dst[0], dst[1], x, y, w, h)
				}
				if w == dst[0] && h == dst[1] {
					continue // exact fit, nothing to compare
				}
				// Aspect must survive to within a pixel of rounding. Integer
				// truncation of the fitted edge bounds the cross-product error
				// by the larger source dimension.
				tol := srcW
				if srcH > tol {
					tol = srcH
				}
				if diff := srcW*h - srcH*w; diff > tol || diff < -tol {
					t.Fatalf("fitRect(%d, %d, %d, %d) = (%d, %d, %d, %d) distorts aspect",
						srcW, srcH, dst[0], dst[1], x, y, w, h)
				}
			}
		}
	}
}
