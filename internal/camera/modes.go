// This file carries no build tag on purpose. Choosing *which* capture mode to
// open is ordinary logic and belongs in the `make test` lane; only the V4L2
// enumeration that feeds it lives behind the `v4l2` tag, in camera.go
// (hard rule 2, same split as controls.go).
package camera

import "fmt"

// fpsTolerance is how far below the target rate still counts as meeting it.
// One frame is enough to absorb NTSC-style rates: a camera advertising
// 30000/1001 (29.97 fps) is a 30 fps camera, and a strict comparison would
// wrongly demote it to the best-effort tier below.
const fpsTolerance = 1.0

// mode is one capture geometry the device advertises. FPS is the highest rate
// the device reports for that geometry; zero means it enumerated the size but
// not its frame intervals, which is treated as "unknown", not "zero".
type mode struct {
	W, H int
	FPS  float64
}

func (m mode) area() int { return m.W * m.H }

// selectMode picks the capture mode to open: the **largest** geometry within
// the cap that still sustains the target frame rate (E-2 — dancers read the
// screen from across a room, so pixels win once the rate is safe).
//
// Frame rate is the constraint and resolution is maximised under it, so a
// camera offering 1600x1200@15 and 1280x720@30 opens 720p30 rather than a
// mode it cannot actually deliver. When nothing reaches the target it falls
// back to the fastest mode available rather than refusing the device: 25 fps
// is closer to the intent than 15, and a choppy mirror beats a black screen.
//
// Selection is independent of enumeration order so a given camera comes up the
// same way on every boot.
func selectMode(modes []mode, maxW, maxH int, targetFPS float64) (mode, error) {
	floor := targetFPS - fpsTolerance
	var atRate, fastest mode
	var haveAtRate, haveFastest bool

	for _, m := range modes {
		if m.W <= 0 || m.H <= 0 || m.W > maxW || m.H > maxH {
			continue
		}
		if m.FPS >= floor && (!haveAtRate || biggerFirst(m, atRate)) {
			atRate, haveAtRate = m, true
		}
		if !haveFastest || fasterFirst(m, fastest) {
			fastest, haveFastest = m, true
		}
	}

	if haveAtRate {
		return atRate, nil
	}
	if haveFastest {
		return fastest, nil // best effort: nothing reaches the target rate
	}
	return mode{}, fmt.Errorf("no MJPEG mode at or below %dx%d", maxW, maxH)
}

// biggerFirst reports whether a beats b on resolution, then on rate. Used once
// the rate target is already met by both, so extra pixels are the win.
func biggerFirst(a, b mode) bool {
	if a.area() != b.area() {
		return a.area() > b.area()
	}
	if a.FPS != b.FPS {
		return a.FPS > b.FPS
	}
	return a.W > b.W // stable tie-break
}

// fasterFirst reports whether a beats b on rate, then on resolution. Used only
// in the fallback tier, where getting closer to the target rate is the win.
func fasterFirst(a, b mode) bool {
	if a.FPS != b.FPS {
		return a.FPS > b.FPS
	}
	if a.area() != b.area() {
		return a.area() > b.area()
	}
	return a.W > b.W // stable tie-break
}
