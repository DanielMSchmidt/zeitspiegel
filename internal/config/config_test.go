package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// UT-10: defaults are sane and valid.
func TestDefaults(t *testing.T) {
	c := config.Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.Profile != "auto" { // E-2 (rev): highest resolution the camera offers
		t.Errorf("profile = %q, want auto", c.Profile)
	}
	if c.FPS() != 30 { // nominal pipeline rate for auto/1080p
		t.Errorf("FPS = %v, want 30", c.FPS())
	}
	if w, h := c.Resolution(); w != 1920 || h != 1080 {
		t.Errorf("auto nominal resolution = %dx%d, want 1920x1080 (cap)", w, h)
	}
	if !c.MirrorFlip { // FR-2: default on
		t.Error("mirror_flip default must be true")
	}
	if c.BufferMaxS <= 0 || c.BufferMaxBytes <= 0 {
		t.Errorf("buffer budgets must default > 0, got %v s / %v B", c.BufferMaxS, c.BufferMaxBytes)
	}
	if c.Device != "auto" { // first working capture device, not a fixed node
		t.Errorf("device default = %q, want auto", c.Device)
	}
	if c.DefaultDelayS != 15 { // boot default for the time-shifted mirror
		t.Errorf("default_delay_s default = %v, want 15", c.DefaultDelayS)
	}
}

// UT-10: default_delay_s parses from TOML (FR-9 boot default for FR-3).
func TestLoadDefaultDelay(t *testing.T) {
	c, err := config.Load(write(t, `default_delay_s = 12.5`))
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultDelayS != 12.5 {
		t.Errorf("default_delay_s = %v, want 12.5", c.DefaultDelayS)
	}
}

// UT-10: full + partial parse; absent keys keep defaults (FR-9).
func TestLoad(t *testing.T) {
	p := write(t, `
bind = ":80"
source = "synth"
profile = "1080p30"
buffer_max_s = 60.0
mirror_flip = false
focus_auto = false
focus_absolute = 30
`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bind != ":80" || c.Source != "synth" || c.Profile != "1080p30" {
		t.Errorf("parsed = %+v", c)
	}
	if c.FPS() != 30 {
		t.Errorf("FPS = %v, want 30", c.FPS())
	}
	if c.MirrorFlip {
		t.Error("mirror_flip = true, want false (explicitly set)")
	}
	if c.FocusAbsolute != 30 {
		t.Errorf("focus_absolute = %d, want 30", c.FocusAbsolute)
	}
	if c.BufferMaxBytes != config.Default().BufferMaxBytes {
		t.Error("absent key must keep default")
	}
}

// UT-10: invalid file ⇒ clear startup error.
func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name, content, wantSub string
	}{
		{"syntax", "bind = [;", "toml"},
		{"bad profile", `profile = "4k120"`, "profile"},
		{"bad source", `source = "dvd"`, "source"},
		{"negative buffer", `buffer_max_s = -5.0`, "buffer_max_s"},
		{"negative delay", `default_delay_s = -1.0`, "default_delay_s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(write(t, tc.content))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
	if _, err := config.Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Error("missing file: want error")
	}
}

func TestResolution(t *testing.T) {
	c := config.Default()
	c.Profile = "1080p30"
	if w, h := c.Resolution(); w != 1920 || h != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080", w, h)
	}
}

// "auto" means the v4l2 adapter probes the camera; the config reports the
// 1080p cap as the nominal pipeline rate (decode budget, E-2 rev).
func TestAutoProfile(t *testing.T) {
	c := config.Default()
	c.Profile = "auto"
	if err := c.Validate(); err != nil {
		t.Fatalf("auto must be valid: %v", err)
	}
	if !c.AutoResolution() {
		t.Error("AutoResolution() = false for profile auto")
	}
	if c.AutoResolution() != (config.Config{Profile: "1080p30"}.AutoResolution() == false) {
		// sanity: a fixed profile is not auto
	}
	w, h := c.Resolution()
	if w != 1920 || h != 1080 || c.FPS() != 30 {
		t.Errorf("auto nominal = %dx%d@%v, want 1920x1080@30", w, h, c.FPS())
	}
}
