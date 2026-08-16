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
	Profile        string  `toml:"profile"` // "auto" | "720p60" | "1080p30" (E-2)
	BufferMaxS     float64 `toml:"buffer_max_s"`
	BufferMaxBytes int64   `toml:"buffer_max_bytes"`
	MirrorFlip     bool    `toml:"mirror_flip"`     // FR-2, default off
	DefaultDelayS  float64 `toml:"default_delay_s"` // FR-3 boot delay; runtime override via API

	// Network keys (FR-15, E-8). Identical on every card — the unit's role
	// is elected at boot and the fleet's size is discovered, not configured,
	// which is what lets one image serve an installation that grows over
	// time. (A legacy `fleet_size` key from the previous release is ignored
	// by TOML decoding, so already-flashed cards keep booting.)
	NetworkManage bool   `toml:"network_manage"` // let the binary drive the radio (true only on the appliance)
	NameFile      string `toml:"name_file"`      // display-name file on the boot partition; empty = identity default

	// StateFile is where runtime settings changed through the API are kept so
	// they survive a restart (FR-18). It belongs on the boot partition beside
	// the name file: the appliance's root is a read-only overlay, so anywhere
	// else is tmpfs and gone at the next power cut. Empty = persistence off,
	// which is the development default — a laptop has no /boot/firmware.
	StateFile string `toml:"state_file"`

	// Development aids, off by default. A field unit is sealed and silent;
	// a bench card can afford to say more, and `make sd-dev` turns these on.
	// LogLevel is slog's ("debug"|"info"|"warn"|"error"); FrameDumpDir writes
	// the frames the unit is actually showing where the log collection picks
	// them up, which is the only way to look at them on a unit nobody can
	// reach over a network.
	LogLevel        string  `toml:"log_level"`
	FrameDumpDir    string  `toml:"frame_dump_dir"`
	FrameDumpEveryS float64 `toml:"frame_dump_every_s"`
	FrameDumpKeep   int     `toml:"frame_dump_keep"`

	// Picture controls (FR-9). Unset — zero — means "not chosen", and the
	// camera's own default stands: an appliance nobody has tuned should not
	// be second-guessing a sensor. Ranges are per device (`v4l2-ctl
	// --list-ctrls`, which the boot capture records), so a value out of range
	// is refused by the device and aborts the open, exactly as a rejected
	// focus value does.
	Saturation int `toml:"saturation"`
	Contrast   int `toml:"contrast"`
	Gamma      int `toml:"gamma"`

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
		Device:         "auto", // first device that actually streams (UVC cameras also enumerate a metadata-only node)
		Profile:        "auto", // highest MJPEG resolution the camera offers, capped at 1080p (E-2 rev)
		BufferMaxS:     120,
		BufferMaxBytes: 1536 << 20, // 1.5 GiB
		// Off (FR-2, changed 2026-08-16): the owner watched it on the real
		// installation and the flipped picture had left and right the wrong
		// way round for a dancer standing in front of it. Which way is right
		// depends on how the camera is mounted, so this is a default rather
		// than a rule — each unit's card can flip its own, and that choice now
		// survives a restart (FR-18).
		MirrorFlip:      false,
		DefaultDelayS:   25, // boot the mirror with a 25 s shift (FR-3 default)
		ExposureAuto:    true,
		LogLevel:        "info",
		FrameDumpEveryS: 5,
		FrameDumpKeep:   30,
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

// MaxBufferSeconds bounds buffer_max_s and default_delay_s (boot config and
// runtime PATCH): far above any real deployment, low enough that
// seconds→time.Duration conversions can never overflow int64 nanoseconds —
// an overflowed (negative) duration budget silently evicts the ring buffer
// down to a single frame.
const MaxBufferSeconds = 86400

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
	if c.BufferMaxS <= 0 || c.BufferMaxS > MaxBufferSeconds {
		return fmt.Errorf("buffer_max_s %v: must be > 0 and ≤ %d", c.BufferMaxS, MaxBufferSeconds)
	}
	if c.BufferMaxBytes <= 0 {
		return fmt.Errorf("buffer_max_bytes %v: must be > 0", c.BufferMaxBytes)
	}
	if c.Bind == "" {
		return fmt.Errorf("bind: must not be empty")
	}
	if c.DefaultDelayS < 0 || c.DefaultDelayS > MaxBufferSeconds {
		return fmt.Errorf("default_delay_s %v: must be ≥ 0 and ≤ %d", c.DefaultDelayS, MaxBufferSeconds)
	}
	return nil
}

// AutoResolution reports whether the camera adapter should probe the device
// instead of using a fixed profile: the largest MJPEG mode that still sustains
// FPS(), capped at MaxAutoWidth/Height (E-2).
func (c Config) AutoResolution() bool { return c.Profile == "auto" }

// MaxAutoWidth/MaxAutoHeight cap auto-detection so a >1080p camera cannot
// blow the Pi 5's software JPEG-decode budget (unvalidated above 1080p).
const (
	MaxAutoWidth  = 1920
	MaxAutoHeight = 1080
)

// FPS returns the nominal frame rate of the profile. For "auto" (30) it is the
// rate the camera adapter probes *for* — the mode it actually opens is what
// status, gap detection and the exporter then use, since a camera may only
// offer 60, or may not reach 30 at all (E-2). This stays the fallback for
// sources that report no mode. The engine selects frames by capture timestamp,
// so a camera drifting from its advertised rate stays correct either way.
func (c Config) FPS() float64 {
	if c.Profile == "720p60" {
		return 60
	}
	return 30 // auto, 1080p30
}

// Resolution returns the nominal capture size of the profile; "auto" reports
// the 1080p cap. The size actually opened is chosen by the camera adapter and
// reported through /api/v1/status, so it may be smaller than this.
func (c Config) Resolution() (w, h int) {
	if c.Profile == "720p60" {
		return 1280, 720
	}
	return MaxAutoWidth, MaxAutoHeight // auto, 1080p30
}
