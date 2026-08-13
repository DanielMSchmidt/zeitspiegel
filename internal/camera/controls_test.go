package camera

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// UT-26
func TestPlannedControls(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want []control
	}{
		{
			// The deployed default: autofocus off, and no measured focus
			// position to pin it to. Sending the unmeasured 0 is what took a
			// mirror's video away — the attached camera's focus_absolute range
			// starts at 1, so the value was refused, the open aborted, and the
			// unit rendered a delay badge over a black frame. An absolute
			// value only means something once S-2 has measured one.
			name: "pinned focus, no measured value, auto exposure",
			cfg:  config.Config{FocusAuto: false, FocusAbsolute: 0, ExposureAuto: true},
			want: []control{
				{Name: ctrlFocusAuto, Value: 0},
				{Name: ctrlExposureAuto, Value: exposureAperturePriority},
			},
		},
		{
			// A measured value is sent, and out-of-range is then the device
			// disagreeing with a deliberate choice — still fatal (below).
			name: "pinned focus with a measured value",
			cfg:  config.Config{FocusAuto: false, FocusAbsolute: 250, ExposureAuto: true},
			want: []control{
				{Name: ctrlFocusAuto, Value: 0},
				{Name: ctrlFocusAbsolute, Value: 250},
				{Name: ctrlExposureAuto, Value: exposureAperturePriority},
			},
		},
		{
			// Picture controls follow the same rule: nothing is sent unless a
			// number was chosen. The camera that came back grey had saturation
			// at its own default of 67 out of 128 — mid-scale, plenty of
			// headroom, and not something to guess at from here.
			name: "picture controls set",
			cfg: config.Config{
				FocusAuto: true, ExposureAuto: true,
				Saturation: 100, Contrast: 40, Gamma: 120,
			},
			want: []control{
				{Name: ctrlFocusAuto, Value: 1},
				{Name: ctrlExposureAuto, Value: exposureAperturePriority},
				{Name: ctrlSaturation, Value: 100},
				{Name: ctrlContrast, Value: 40},
				{Name: ctrlGamma, Value: 120},
			},
		},
		{
			// Unset picture controls leave the camera's own defaults alone,
			// which is what an appliance that has not been tuned should do.
			name: "picture controls unset",
			cfg:  config.Config{FocusAuto: true, ExposureAuto: true},
			want: []control{
				{Name: ctrlFocusAuto, Value: 1},
				{Name: ctrlExposureAuto, Value: exposureAperturePriority},
			},
		},
		{
			// The same trap on the exposure side, latent until someone turns
			// auto exposure off.
			name: "manual exposure, no measured value",
			cfg:  config.Config{FocusAuto: true, ExposureAuto: false, ExposureAbsolute: 0},
			want: []control{
				{Name: ctrlFocusAuto, Value: 1},
				{Name: ctrlExposureAuto, Value: exposureManual},
			},
		},
		{
			name: "auto focus omits the absolute value",
			cfg:  config.Config{FocusAuto: true, FocusAbsolute: 250, ExposureAuto: true},
			want: []control{
				{Name: ctrlFocusAuto, Value: 1},
				{Name: ctrlExposureAuto, Value: exposureAperturePriority},
			},
		},
		{
			name: "everything pinned",
			cfg:  config.Config{FocusAuto: false, FocusAbsolute: 250, ExposureAuto: false, ExposureAbsolute: 312},
			want: []control{
				{Name: ctrlFocusAuto, Value: 0},
				{Name: ctrlFocusAbsolute, Value: 250},
				{Name: ctrlExposureAuto, Value: exposureManual},
				{Name: ctrlExposureAbsolute, Value: 312},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plannedControls(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("plannedControls() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("plannedControls()[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// recorder is a fake control setter: it records every control it is asked to
// set and fails the ones named in fail.
type recorder struct {
	set  []string
	fail map[string]error
}

func (r *recorder) apply(c control) error {
	if err, ok := r.fail[c.Name]; ok {
		return err
	}
	r.set = append(r.set, c.Name)
	return nil
}

// UT-26: a fixed-focus camera implements no focus controls at all. Skipping
// them is what makes the recommended wide-angle cameras usable
// (docs/HARDWARE.md); aborting the open is the bug this replaces.
func TestApplyPlanSkipsUnsupportedControls(t *testing.T) {
	// A measured focus value, so the plan actually contains the control whose
	// absence this test is about (an unmeasured 0 is never planned at all).
	cfg := config.Config{FocusAbsolute: 250, ExposureAuto: true}
	rec := &recorder{fail: map[string]error{
		// go4vl reports an unimplemented control id as a bad argument from
		// VIDIOC_QUERYCTRL; the adapter translates it to this sentinel.
		ctrlFocusAuto:     fmt.Errorf("query control: %w", errUnsupportedControl),
		ctrlFocusAbsolute: errUnsupportedControl,
	}}

	skipped, err := applyPlan(plannedControls(cfg), rec.apply)
	if err != nil {
		t.Fatalf("applyPlan() error = %v, want nil (unsupported controls are not fatal)", err)
	}
	want := []string{ctrlFocusAuto, ctrlFocusAbsolute}
	if strings.Join(skipped, ",") != strings.Join(want, ",") {
		t.Fatalf("skipped = %v, want %v", skipped, want)
	}
	// The controls the device *does* implement must still be applied.
	if strings.Join(rec.set, ",") != ctrlExposureAuto {
		t.Fatalf("applied = %v, want [%s]", rec.set, ctrlExposureAuto)
	}
}

// UT-26
func TestApplyPlanAllSupported(t *testing.T) {
	cfg := config.Config{FocusAbsolute: 250, ExposureAbsolute: 312}
	rec := &recorder{}

	skipped, err := applyPlan(plannedControls(cfg), rec.apply)
	if err != nil {
		t.Fatalf("applyPlan() error = %v, want nil", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	want := []string{ctrlFocusAuto, ctrlFocusAbsolute, ctrlExposureAuto, ctrlExposureAbsolute}
	if strings.Join(rec.set, ",") != strings.Join(want, ",") {
		t.Fatalf("applied = %v, want %v", rec.set, want)
	}
}

// UT-26: a control that exists but genuinely fails is still fatal — an I/O
// error or an out-of-range value must not be silently swallowed.
func TestApplyPlanRealFailureAborts(t *testing.T) {
	cfg := config.Config{FocusAbsolute: 250, ExposureAuto: true}
	sentinel := errors.New("input/output error")
	rec := &recorder{fail: map[string]error{ctrlFocusAbsolute: sentinel}}

	skipped, err := applyPlan(plannedControls(cfg), rec.apply)
	if err == nil {
		t.Fatal("applyPlan() error = nil, want a failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("applyPlan() error = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), ctrlFocusAbsolute) {
		t.Fatalf("applyPlan() error = %q, want it to name %s", err, ctrlFocusAbsolute)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}
	// It aborts at the failure rather than carrying on.
	if strings.Join(rec.set, ",") != ctrlFocusAuto {
		t.Fatalf("applied = %v, want [%s]", rec.set, ctrlFocusAuto)
	}
}
