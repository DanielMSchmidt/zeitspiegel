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
	DelayMaxS      float64 `toml:"delay_max_s"`     // max delay the slider/PUT /api/v1/delay accepts; ≤ BufferMaxS
	MirrorFlip     bool    `toml:"mirror_flip"`     // FR-2, default on
	DefaultDelayS  float64 `toml:"default_delay_s"` // FR-3 boot delay; runtime override via API

	// camera controls (FR-9; values measured in spike S-2)
	FocusAuto        bool `toml:"focus_auto"` // default off: pin focus
	FocusAbsolute    int  `toml:"focus_absolute"`
	ExposureAuto     bool `toml:"exposure_auto"`
	ExposureAbsolute int  `toml:"exposure_absolute"`
}

// DefaultBufferMaxS is the default export-window / ring-buffer duration in
// seconds. Sized for ~12 min of 1080p30 MJPEG (~6 MB/s estimate) on an
// 8 GiB Pi 5 with ~1.4 GiB slack for ffmpeg export and OS overhead. If the
// real-hardware bitrate (spike S-1 / M3) shows the Kiyo runs hotter than
// the estimate in ARCHITECTURE §3, lower this value — every other default
// follows from it.
const DefaultBufferMaxS = 720

// DefaultBufferMaxBytes is the default byte cap for the ring buffer.
// 5 GiB: large enough that DefaultBufferMaxS is the binding eviction
// constraint at ~6 MB/s, small enough that a sustained bitrate spike
// (busy scene) evicts early instead of crowding out the ffmpeg export
// subprocess and the 200 MB NFR-2 reserve.
const DefaultBufferMaxBytes = 5 << 30 // 5 GiB

// DefaultDelayMaxS is the default user-facing delay cap. Independent of
// DefaultBufferMaxS so the delay slider can stay at a familiar value
// (2 min) while the buffer retains 12 min for export.
const DefaultDelayMaxS = 120

// Default returns the boot defaults (deploy/config.toml overrides for the Pi).
func Default() Config {
	return Config{
		Bind:           ":8080",
		Source:         "camera",
		Device:         "auto", // first device that actually streams (the Kiyo also enumerates a metadata-only node)
		Profile:        "auto", // highest MJPEG resolution the camera offers, capped at 1080p (E-2 rev)
		BufferMaxS:     DefaultBufferMaxS,
		BufferMaxBytes: DefaultBufferMaxBytes,
		DelayMaxS:      DefaultDelayMaxS,
		MirrorFlip:     true,
		DefaultDelayS:  15, // boot the mirror with a 15 s shift (FR-3 default)
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
	if c.DelayMaxS <= 0 {
		return fmt.Errorf("delay_max_s %v: must be > 0", c.DelayMaxS)
	}
	if c.DelayMaxS > c.BufferMaxS {
		return fmt.Errorf("delay_max_s %v: must be ≤ buffer_max_s %v", c.DelayMaxS, c.BufferMaxS)
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
