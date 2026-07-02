package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// The dev camera path (no v4l2 build tag) feeds ffmpeg per OS. Profile
// "auto" must not force a size — ffmpeg cannot probe like the go4vl path,
// and pinning the 1080p nominal fails cameras without that exact mode.
func TestFFCamInputArgs(t *testing.T) {
	cfg := config.Default() // device "auto", profile auto

	mac, err := ffcamInput("darwin", cfg, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"avfoundation", "default:none"} {
		if !slices.ContainsFunc(mac, func(a string) bool { return strings.Contains(a, want) }) {
			t.Errorf("darwin args %v missing %q", mac, want)
		}
	}
	if slices.Contains(mac, "-video_size") {
		t.Errorf("darwin args %v force a size for profile auto", mac)
	}

	lin, err := ffcamInput("linux", cfg, "/dev/video2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v4l2", "/dev/video2"} {
		if !slices.Contains(lin, want) {
			t.Errorf("linux args %v missing %q", lin, want)
		}
	}
	if slices.Contains(lin, "-video_size") {
		t.Errorf("linux args %v force a size for profile auto", lin)
	}

	cfg.Profile = "720p60" // a fixed profile pins the size
	fixed, err := ffcamInput("linux", cfg, "/dev/video0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fixed, "1280x720") {
		t.Errorf("720p60 args %v missing 1280x720", fixed)
	}

	if _, err := ffcamInput("windows", cfg, "auto"); err == nil {
		t.Error("unsupported OS must error")
	}
}

// resolveDevice picks the explicit device or the first enumerated one.
func TestResolveDevice(t *testing.T) {
	if got, _ := resolveDevice("/dev/video7", nil); got != "/dev/video7" {
		t.Errorf("explicit device overridden: %q", got)
	}
	got, err := resolveDevice("auto", []string{"/dev/video1", "/dev/video0"})
	if err != nil || got != "/dev/video0" {
		t.Errorf("auto = (%q, %v), want first sorted device", got, err)
	}
	if _, err := resolveDevice("auto", nil); err == nil {
		t.Error("auto with no devices must error")
	}
}
