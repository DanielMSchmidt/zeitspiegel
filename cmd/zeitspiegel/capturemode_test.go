package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// UT-29: the pipeline reports and budgets against the mode the camera actually
// opened, falling back to the profile nominal when the source does not report
// one (synth, and the ffmpeg dev camera).
func TestCaptureModeFallsBackToProfileNominal(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		set     *captureMode
		wantW   int
		wantH   int
		wantFPS float64
	}{
		{"auto, no source mode", "auto", nil, 1920, 1080, 30},
		{"720p60, no source mode", "720p60", nil, 1280, 720, 60},
		{"1080p30, no source mode", "1080p30", nil, 1920, 1080, 30},
		{
			// The whole point: auto probed down to 720p to hold 30 fps, and
			// the API must say so instead of claiming 1920x1080.
			"auto, camera opened 720p30", "auto",
			&captureMode{W: 1280, H: 720, FPS: 30}, 1280, 720, 30,
		},
		{
			// A 60 fps capture reported as the nominal 30 makes clips play at
			// half speed (FR-5).
			"auto, camera opened 1080p60", "auto",
			&captureMode{W: 1920, H: 1080, FPS: 60}, 1920, 1080, 60,
		},
		{
			"auto, camera opened 4:3", "auto",
			&captureMode{W: 1600, H: 1200, FPS: 30}, 1600, 1200, 30,
		},
		{
			// Device enumerated sizes but not intervals: geometry is known,
			// rate is not, so only the rate falls back.
			"unknown rate falls back, geometry does not", "auto",
			&captureMode{W: 1600, H: 1200, FPS: 0}, 1600, 1200, 30,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m modeStore
			if tc.set != nil {
				m.Set(tc.set.W, tc.set.H, tc.set.FPS)
			}
			w, h := m.resolution(tc.profile)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("resolution(%q) = %dx%d, want %dx%d", tc.profile, w, h, tc.wantW, tc.wantH)
			}
			if fps := m.fps(tc.profile); fps != tc.wantFPS {
				t.Errorf("fps(%q) = %v, want %v", tc.profile, fps, tc.wantFPS)
			}
		})
	}
}

// UT-29: a source that stops reporting a mode (camera unplugged, synth takes
// over on reopen) must not leave the previous camera's mode behind.
func TestCaptureModeClearedOnReopen(t *testing.T) {
	var m modeStore
	m.Set(1280, 720, 30)
	if w, _ := m.resolution("auto"); w != 1280 {
		t.Fatalf("resolution width = %d, want 1280", w)
	}
	m.Clear()
	w, h := m.resolution("auto")
	if w != 1920 || h != 1080 {
		t.Errorf("after Clear: resolution = %dx%d, want the 1920x1080 nominal", w, h)
	}
	if fps := m.fps("auto"); fps != 30 {
		t.Errorf("after Clear: fps = %v, want the 30 nominal", fps)
	}
}

// UT-29: /api/v1/status carries the live mode through, not the profile string.
func TestStatusReportsLiveCaptureMode(t *testing.T) {
	newStatus := func(m *modeStore) *sysStatus {
		cfg := config.Default()
		buf := ringbuf.New(time.Minute, 1<<20)
		return &sysStatus{
			start: time.Now(),
			cfg:   cfg,
			store: &runtimeStore{rt: cfg.Runtime(), buf: buf, restart: &atomic.Bool{}},
			buf:   buf,
			eng:   engine.New(buf),
			sup:   capture.New(capture.Options{}),
			mode:  m,
		}
	}

	// No source mode yet (boot, or synth): the nominal.
	if st := newStatus(&modeStore{}).Status(); st.Resolution != "1920x1080" || st.FPS != 30 {
		t.Errorf("without a source mode: resolution = %q, fps = %v; want 1920x1080 and 30", st.Resolution, st.FPS)
	}

	live := &modeStore{}
	live.Set(1280, 720, 60)
	st := newStatus(live).Status()
	if st.Resolution != "1280x720" {
		t.Errorf("resolution = %q, want 1280x720 (what the camera opened)", st.Resolution)
	}
	if st.FPS != 60 {
		t.Errorf("fps = %v, want 60 (what the camera opened)", st.FPS)
	}
}
