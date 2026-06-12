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
	if c.Profile != "720p60" { // E-2: temporal > spatial resolution
		t.Errorf("profile = %q, want 720p60", c.Profile)
	}
	if c.FPS() != 60 {
		t.Errorf("FPS = %v, want 60", c.FPS())
	}
	if !c.MirrorFlip { // FR-2: default on
		t.Error("mirror_flip default must be true")
	}
	if c.BufferMaxS <= 0 || c.BufferMaxBytes <= 0 {
		t.Errorf("buffer budgets must default > 0, got %v s / %v B", c.BufferMaxS, c.BufferMaxBytes)
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
