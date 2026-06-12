package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// The dev camera path (no v4l2 build tag) feeds ffmpeg per OS.
func TestFFCamInputArgs(t *testing.T) {
	cfg := config.Default() // device "auto", 720p60

	mac, err := ffcamInput("darwin", cfg, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"avfoundation", "default:none", "1280x720"} {
		if !slices.ContainsFunc(mac, func(a string) bool { return strings.Contains(a, want) }) {
			t.Errorf("darwin args %v missing %q", mac, want)
		}
	}

	lin, err := ffcamInput("linux", cfg, "/dev/video2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v4l2", "/dev/video2", "1280x720"} {
		if !slices.Contains(lin, want) {
			t.Errorf("linux args %v missing %q", lin, want)
		}
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
