// Package config parses the TOML boot configuration (FR-9). Runtime changes
// go through the API; the file only provides boot defaults.
package config

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// Config is the boot configuration. Field defaults come from Default();
// absent TOML keys keep them.
type Config struct {
	Bind           string  `toml:"bind"`
	Source         string  `toml:"source"` // "camera" | "synth"
	Device         string  `toml:"device"`
	ClipDir        string  `toml:"clip_dir"` // empty = system temp dir
	Profile        string  `toml:"profile"`  // "720p60" | "1080p30" (E-2)
	BufferMaxS     float64 `toml:"buffer_max_s"`
	BufferMaxBytes int64   `toml:"buffer_max_bytes"`
	MirrorFlip     bool    `toml:"mirror_flip"`     // FR-2, default on
	DefaultDelayS  float64 `toml:"default_delay_s"` // FR-3 boot delay; runtime override via API

	// camera controls (FR-9; values measured in spike S-2)
	FocusAuto        bool `toml:"focus_auto"` // default off: pin focus
	FocusAbsolute    int  `toml:"focus_absolute"`
	ExposureAuto     bool `toml:"exposure_auto"`
	ExposureAbsolute int  `toml:"exposure_absolute"`
}

// Default returns the boot defaults (deploy/config.toml overrides for the Pi).
func Default() Config {
	return Config{
		Bind:           ":8080",
		Source:         "camera",
		Device:         "auto", // first device that actually streams (the Kiyo also enumerates a metadata-only node)
		Profile:        "auto", // highest MJPEG resolution the camera offers, capped at 1080p (E-2 rev)
		BufferMaxS:     120,
		BufferMaxBytes: 1536 << 20, // 1.5 GiB
		MirrorFlip:     true,
		DefaultDelayS:  30, // boot the mirror with a 30 s shift (FR-3 default)
		ExposureAuto:   true,
	}
}

// Load reads path over the defaults and validates. Errors are meant to be
// clear startup errors (UT-10).
func Load(path string) (Config, error) {
	c := Default()
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Validate checks value ranges and enums.
func (c Config) Validate() error {
	switch c.Profile {
	case "auto", "720p60", "1080p30":
	default:
		return fmt.Errorf("profile %q: must be auto, 720p60 or 1080p30", c.Profile)
	}
	switch c.Source {
	case "camera", "synth":
	default:
		return fmt.Errorf("source %q: must be camera or synth", c.Source)
	}
	if c.BufferMaxS <= 0 {
		return fmt.Errorf("buffer_max_s %v: must be > 0", c.BufferMaxS)
	}
	if c.BufferMaxBytes <= 0 {
		return fmt.Errorf("buffer_max_bytes %v: must be > 0", c.BufferMaxBytes)
	}
	if c.Bind == "" {
		return fmt.Errorf("bind: must not be empty")
	}
	if c.DefaultDelayS < 0 {
		return fmt.Errorf("default_delay_s %v: must be ≥ 0", c.DefaultDelayS)
	}
	return nil
}

// AutoResolution reports whether the camera adapter should probe the device
// for its highest MJPEG mode instead of using a fixed profile (capped at the
// nominal 1080p, see MaxAutoWidth/Height).
func (c Config) AutoResolution() bool { return c.Profile == "auto" }

// MaxAutoWidth/MaxAutoHeight cap auto-detection so a >1080p camera cannot
// blow the Pi 5's software JPEG-decode budget (unvalidated above 1080p).
const (
	MaxAutoWidth  = 1920
	MaxAutoHeight = 1080
)

// FPS returns the nominal pipeline frame rate of the profile. For "auto" it
// is the 1080p nominal (30) — the display tick and exporter use this; the
// engine selects frames by capture timestamp, so a camera delivering a
// slightly different rate stays correct.
func (c Config) FPS() float64 {
	if c.Profile == "720p60" {
		return 60
	}
	return 30 // auto, 1080p30
}

// Resolution returns the nominal capture size of the profile; "auto" reports
// the 1080p cap (the actual mode is chosen by the camera adapter at open).
func (c Config) Resolution() (w, h int) {
	if c.Profile == "720p60" {
		return 1280, 720
	}
	return MaxAutoWidth, MaxAutoHeight // auto, 1080p30
}
