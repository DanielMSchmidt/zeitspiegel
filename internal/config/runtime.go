package config

import (
	"errors"
	"fmt"
)

// ErrInvalid marks validation failures of runtime patches (maps to 422).
var ErrInvalid = errors.New("invalid value")

// Runtime is the API-visible, runtime-changeable subset of the configuration
// (REQUIREMENTS §3, GET/PATCH /api/v1/config).
type Runtime struct {
	MirrorFlip       bool    `json:"mirror_flip"`
	Profile          string  `json:"profile"`
	BufferMaxS       float64 `json:"buffer_max_s"`
	FocusAuto        bool    `json:"focus_auto"`
	FocusAbsolute    int     `json:"focus_absolute"`
	ExposureAuto     bool    `json:"exposure_auto"`
	ExposureAbsolute int     `json:"exposure_absolute"`
}

// Runtime projects the boot config onto its runtime-changeable subset.
func (c Config) Runtime() Runtime {
	return Runtime{
		MirrorFlip:       c.MirrorFlip,
		Profile:          c.Profile,
		BufferMaxS:       c.BufferMaxS,
		FocusAuto:        c.FocusAuto,
		FocusAbsolute:    c.FocusAbsolute,
		ExposureAuto:     c.ExposureAuto,
		ExposureAbsolute: c.ExposureAbsolute,
	}
}

// Patch is a partial runtime update; nil fields stay unchanged.
type Patch struct {
	MirrorFlip       *bool    `json:"mirror_flip"`
	Profile          *string  `json:"profile"`
	BufferMaxS       *float64 `json:"buffer_max_s"`
	FocusAuto        *bool    `json:"focus_auto"`
	FocusAbsolute    *int     `json:"focus_absolute"`
	ExposureAuto     *bool    `json:"exposure_auto"`
	ExposureAbsolute *int     `json:"exposure_absolute"`
}

// WithPatch returns a validated copy with the patch applied.
func (r Runtime) WithPatch(p Patch) (Runtime, error) {
	if p.MirrorFlip != nil {
		r.MirrorFlip = *p.MirrorFlip
	}
	if p.Profile != nil {
		switch *p.Profile {
		case "auto", "720p60", "1080p30":
			r.Profile = *p.Profile
		default:
			return Runtime{}, fmt.Errorf("profile %q: %w (must be auto, 720p60 or 1080p30)", *p.Profile, ErrInvalid)
		}
	}
	if p.BufferMaxS != nil {
		if *p.BufferMaxS <= 0 {
			return Runtime{}, fmt.Errorf("buffer_max_s %v: %w (must be > 0)", *p.BufferMaxS, ErrInvalid)
		}
		r.BufferMaxS = *p.BufferMaxS
	}
	if p.FocusAuto != nil {
		r.FocusAuto = *p.FocusAuto
	}
	if p.FocusAbsolute != nil {
		r.FocusAbsolute = *p.FocusAbsolute
	}
	if p.ExposureAuto != nil {
		r.ExposureAuto = *p.ExposureAuto
	}
	if p.ExposureAbsolute != nil {
		r.ExposureAbsolute = *p.ExposureAbsolute
	}
	return r, nil
}
