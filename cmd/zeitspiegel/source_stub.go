//go:build !v4l2

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/ffcam"
)

// openCamera without the v4l2 build tag: dev camera via an ffmpeg
// subprocess (avfoundation on macOS, v4l2 demuxer on Linux). Camera controls
// (focus/exposure pinning, FR-9) are NOT applied on this path — the
// production appliance builds with -tags v4l2 and uses go4vl.
func openCamera(ctx context.Context, cfg config.Config) (capture.Source, error) {
	device := cfg.Device
	if runtime.GOOS == "darwin" {
		if device == "" || device == "auto" {
			device = "default" // avfoundation's default camera
		}
	} else {
		nodes, _ := filepath.Glob("/dev/video*")
		var err error
		if device, err = resolveDevice(device, nodes); err != nil {
			return nil, fmt.Errorf("camera: %w", err)
		}
	}
	input, err := ffcamInput(runtime.GOOS, cfg, device)
	if err != nil {
		return nil, err
	}
	return ffcam.Open(ctx, ffcam.Options{InputArgs: input, OutputFPS: cfg.FPS()})
}
