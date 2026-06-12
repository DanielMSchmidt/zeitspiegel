package main

import (
	"fmt"
	"sort"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// ffcamInput builds the ffmpeg input arguments for the dev camera path
// (builds without the v4l2 tag). Input framerate is pinned to 30 — the
// lowest common denominator webcams accept — and the ffcam output resampler
// (-r) brings the stream up to the profile's nominal rate.
func ffcamInput(goos string, cfg config.Config, device string) ([]string, error) {
	w, h := cfg.Resolution()
	size := fmt.Sprintf("%dx%d", w, h)
	switch goos {
	case "darwin":
		return []string{
			"-f", "avfoundation", "-framerate", "30",
			"-video_size", size, "-i", device + ":none",
		}, nil
	case "linux":
		return []string{
			"-f", "v4l2", "-framerate", "30",
			"-video_size", size, "-i", device,
		}, nil
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
