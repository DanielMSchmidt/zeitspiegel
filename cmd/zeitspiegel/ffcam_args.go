package main

import (
	"fmt"
	"sort"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// ffcamInput builds the ffmpeg input arguments for the dev camera path
// (builds without the v4l2 tag). Input framerate is pinned to 30 — the
// lowest common denominator webcams accept — and the ffcam output resampler
// (-r) brings the stream up to the profile's nominal rate. Profile "auto"
// leaves the device's default size: ffmpeg cannot probe for the best mode
// like the go4vl path does, and forcing the 1080p nominal would fail any
// camera without that exact mode.
func ffcamInput(goos string, cfg config.Config, device string) ([]string, error) {
	var sizeArgs []string
	if !cfg.AutoResolution() {
		w, h := cfg.Resolution()
		sizeArgs = []string{"-video_size", fmt.Sprintf("%dx%d", w, h)}
	}
	switch goos {
	case "darwin":
		args := []string{"-f", "avfoundation", "-framerate", "30"}
		args = append(args, sizeArgs...)
		return append(args, "-i", device+":none"), nil
	case "linux":
		args := []string{"-f", "v4l2", "-framerate", "30"}
		args = append(args, sizeArgs...)
		return append(args, "-i", device), nil
	default:
		return nil, fmt.Errorf("no camera support on %s without the v4l2 build tag", goos)
	}
}

// resolveDevice maps "auto" to the first enumerated capture device.
func resolveDevice(device string, candidates []string) (string, error) {
	if device != "" && device != "auto" {
		return device, nil
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no camera devices found")
	}
	sorted := append([]string(nil), candidates...)
	sort.Strings(sorted)
	return sorted[0], nil
}
