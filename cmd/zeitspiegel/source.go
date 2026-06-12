package main

import (
	"context"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

// openSource picks the frame source. Camera support is build-tagged
// (openCamera lives in source_v4l2.go / source_stub.go).
func openSource(ctx context.Context, boot config.Config, rt config.Runtime) (capture.Source, error) {
	if boot.Source == "synth" {
		c := boot
		c.Profile = rt.Profile
		return newSynthSource(c), nil
	}
	c := boot
	c.Profile = rt.Profile
	c.FocusAuto = rt.FocusAuto
	c.FocusAbsolute = rt.FocusAbsolute
	c.ExposureAuto = rt.ExposureAuto
	c.ExposureAbsolute = rt.ExposureAbsolute
	return openCamera(ctx, c)
}

// synthSource paces the deterministic synth generator in real time (demo
// mode / `make run-synth`): one frame per tick, stamped with the wall clock
// at emission (this is its capture time as far as the pipeline is
// concerned).
type synthSource struct {
	src  *synth.Source
	tick *time.Ticker
}

func newSynthSource(cfg config.Config) *synthSource {
	fps := cfg.FPS()
	return &synthSource{
		src:  synth.NewSource(fps, time.Now()),
		tick: time.NewTicker(time.Duration(float64(time.Second) / fps)),
	}
}

func (s *synthSource) ReadFrame(ctx context.Context) (frame.Frame, error) {
	select {
	case <-ctx.Done():
		return frame.Frame{}, ctx.Err()
	case <-s.tick.C:
		f := s.src.Next()
		return frame.Frame{Seq: f.Seq, CaptureTS: time.Now(), JPEG: f.JPEG}, nil
	}
}

func (s *synthSource) Close() error {
	s.tick.Stop()
	return nil
}
