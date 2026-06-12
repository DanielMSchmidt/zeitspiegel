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
	MirrorFlip     bool    `toml:"mirror_flip"` // FR-2, default on

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
		Profile:        "720p60",
		BufferMaxS:     120,
		BufferMaxBytes: 1536 << 20, // 1.5 GiB
		MirrorFlip:     true,
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
	case "720p60", "1080p30":
	default:
		return fmt.Errorf("profile %q: must be 720p60 or 1080p30", c.Profile)
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
	return nil
}

// FPS returns the nominal frame rate of the profile.
func (c Config) FPS() float64 {
	if c.Profile == "1080p30" {
		return 30
	}
	return 60
}

// Resolution returns the capture size of the profile.
func (c Config) Resolution() (w, h int) {
	if c.Profile == "1080p30" {
		return 1920, 1080
	}
	return 1280, 720
}
