// This file carries no build tag on purpose. The decision of *which* controls
// to pin, and what to do when a device does not implement one, is ordinary
// logic and belongs in the `make test` lane (hard rule 2 keeps only the cgo
// V4L2 calls behind the `v4l2` tag, in camera.go).
package camera

import (
	"errors"
	"fmt"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// Control names, as they appear in the config file and in log lines.
const (
	ctrlFocusAuto        = "focus_auto"
	ctrlFocusAbsolute    = "focus_absolute"
	ctrlExposureAuto     = "exposure_auto"
	ctrlExposureAbsolute = "exposure_absolute"
)

// UVC exposure_auto is a menu, not a boolean.
const (
	exposureManual           = 1
	exposureAperturePriority = 3
)

// errUnsupportedControl marks a control the device does not implement. The
// V4L2 adapter translates the driver's error into this sentinel so the skip
// logic below never has to know about go4vl (which discards the raw errno and
// reports its own error values).
var errUnsupportedControl = errors.New("control not implemented by device")

// control is one V4L2 control to pin at open time (FR-9).
type control struct {
	Name  string
	Value int32
}

// plannedControls lists the controls to pin for cfg, in application order.
// The absolute value is only sent when the matching auto mode is off — pinning
// focus while autofocus is still running is meaningless.
func plannedControls(cfg config.Config) []control {
	plan := []control{{Name: ctrlFocusAuto, Value: boolValue(cfg.FocusAuto)}}
	if !cfg.FocusAuto {
		plan = append(plan, control{Name: ctrlFocusAbsolute, Value: int32(cfg.FocusAbsolute)})
	}
	expo := int32(exposureAperturePriority)
	if !cfg.ExposureAuto {
		expo = exposureManual
	}
	plan = append(plan, control{Name: ctrlExposureAuto, Value: expo})
	if !cfg.ExposureAuto {
		plan = append(plan, control{Name: ctrlExposureAbsolute, Value: int32(cfg.ExposureAbsolute)})
	}
	return plan
}

// applyPlan sets each control through set, returning the names of the ones the
// device does not implement. Those are skipped rather than fatal: the
// wide-angle fixed-focus cameras this appliance is built around (E-9,
// docs/HARDWARE.md) expose no focus controls at all, and refusing to open them
// would be refusing the recommended camera. Any other failure still aborts —
// an I/O error or an out-of-range value is a real problem, not a missing
// feature. The caller is expected to report the skipped names so a control
// silently dropped from the config stays visible (NFR-8).
func applyPlan(plan []control, set func(control) error) ([]string, error) {
	var skipped []string
	for _, c := range plan {
		err := set(c)
		if err == nil {
			continue
		}
		if errors.Is(err, errUnsupportedControl) {
			skipped = append(skipped, c.Name)
			continue
		}
		return skipped, fmt.Errorf("camera: set %s=%d: %w", c.Name, c.Value, err)
	}
	return skipped, nil
}

func boolValue(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
