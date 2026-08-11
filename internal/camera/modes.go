// This file carries no build tag on purpose. Choosing *which* capture mode to
// open is ordinary logic and belongs in the `make test` lane; only the V4L2
// enumeration that feeds it lives behind the `v4l2` tag, in camera.go
// (hard rule 2, same split as controls.go).
package camera

import "fmt"

// fpsFloorMargin is how far below the target rate a mode may fall and still
// count as meeting it — 30 - 5 = 25 fps.
//
// It is deliberately wider than a rounding tolerance. It absorbs NTSC and PAL
// rates (30000/1001 = 29.97, and 25) so those cameras are not demoted, and it
// buys resolution: holding 1080p at 25 fps is worth more in a mirror than
// dropping to 720p to gain five frames. Below 25, motion starts reading as
// stutter, which defeats the point of reviewing movement.
const fpsFloorMargin = 5.0

// mode is one capture geometry the device advertises. FPS is the highest rate
// the device reports for that geometry; zero means it enumerated the size but
// not its frame intervals, which is treated as "unknown", not "zero".
type mode struct {
	W, H int
	FPS  float64
}

func (m mode) area() int { return m.W * m.H }

// selectMode picks the capture mode to open: the **largest** geometry within
// the cap whose rate clears the floor (target minus fpsFloorMargin, i.e.
// 25 fps for a 30 fps target). Among modes that clear it, pixels win (E-2 —
// dancers read the screen from across a room).
//
// Frame rate is the constraint and resolution is maximised under it, so a
// camera offering 1600x1200@15 and 1280x720@30 opens 720p30 rather than a mode
// it cannot actually deliver — while one offering 1920x1080@25 keeps the
// 1080p, because 25 fps still reads as motion. When nothing clears the floor
// it falls back to the fastest mode available rather than refusing the device:
// a choppy mirror beats a black screen.
//
// Selection is independent of enumeration order so a given camera comes up the
// same way on every boot.
func selectMode(modes []mode, maxW, maxH int, targetFPS float64) (mode, error) {
	floor := targetFPS - fpsFloorMargin
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
