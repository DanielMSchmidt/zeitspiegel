package main

import "sync/atomic"

// captureMode is the geometry and frame rate a capture source actually opened.
// On the "auto" profile the camera adapter probes for these (E-2), so they can
// differ from the profile nominal — that is the point.
type captureMode struct {
	W, H int
	FPS  float64
}

// modeStore holds the live capture mode for readers outside the capture
// goroutine (the status handler, the export rate, gap detection). The capture
// supervisor is the single writer, on every source open; an atomic pointer
// keeps readers lock-free and always sees a whole mode, never a torn one.
//
// The zero value is "no source has reported a mode", which is the normal state
// for synth and the ffmpeg dev camera — those callers fall back to the
// profile nominal.
type modeStore struct{ v atomic.Pointer[captureMode] }

func (m *modeStore) Set(w, h int, fps float64) {
	m.v.Store(&captureMode{W: w, H: h, FPS: fps})
}

// Clear drops the recorded mode, so a source that does not report one cannot
// inherit the previous camera's values across a reopen.
func (m *modeStore) Clear() { m.v.Store(nil) }

// fps returns the live capture rate, falling back to the profile nominal when
// no source has reported one or the device did not enumerate its intervals.
// Feeding the nominal 30 to the exporter while the camera runs at 60 makes
// clips play at half speed (FR-5), which is why this is consulted per export.
func (m *modeStore) fps(profile string) float64 {
	if c := m.v.Load(); c != nil && c.FPS > 0 {
		return c.FPS
	}
	return profileFPS(profile)
}

// resolution returns the live capture geometry, falling back to the profile
// nominal. Unlike the rate, geometry is known whenever a mode was recorded.
func (m *modeStore) resolution(profile string) (int, int) {
	if c := m.v.Load(); c != nil && c.W > 0 && c.H > 0 {
		return c.W, c.H
	}
	return profileResolution(profile)
}
