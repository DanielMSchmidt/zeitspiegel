//go:build v4l2

// Package camera is the thin V4L2 adapter over go4vl (ARCHITECTURE §3).
// cgo lives only here and in internal/screen (hard rule 2); core packages
// never import this package.
package camera

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"time"

	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Camera implements capture.Source over a UVC device in native MJPEG (D2).
type Camera struct {
	dev     *device.Device
	frames  <-chan []byte
	seq     uint64
	skipped []string
	mode    mode
}

// Open configures and starts the device per the boot config (FR-9: pinned
// focus / exposure to prevent hunting during movement). Device "auto" (or
// empty) tries /dev/video* in order and takes the first node that opens,
// configures and streams — UVC cameras commonly also enumerate a
// metadata-only node that fails here and is skipped.
func Open(ctx context.Context, cfg config.Config) (*Camera, error) {
	if cfg.Device != "" && cfg.Device != "auto" {
		return openDevice(ctx, cfg, cfg.Device)
	}
	nodes, err := filepath.Glob("/dev/video*")
	if err != nil || len(nodes) == 0 {
		return nil, fmt.Errorf("camera: no /dev/video* devices found")
	}
	sort.Strings(nodes)
	var errs []error
	for _, node := range nodes {
		cam, err := openDevice(ctx, cfg, node)
		if err == nil {
			return cam, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("camera: no usable device among %v: %w", nodes, errors.Join(errs...))
}

// openDevice configures and starts one specific node.
func openDevice(ctx context.Context, cfg config.Config, path string) (*Camera, error) {
	w, h := cfg.Resolution()
	fps := cfg.FPS()
	if cfg.AutoResolution() {
		best, err := probeBestMode(path, cfg.FPS())
		if err != nil {
			return nil, fmt.Errorf("camera: probe %s: %w", path, err)
		}
		w, h = best.W, best.H
		if best.FPS > 0 {
			// Ask the device for the rate it actually advertises, not the
			// profile nominal — requesting 30 from a 15 fps mode is the bug
			// this replaces. Rounded, so 30000/1001 asks for 30, not 29.
			fps = best.FPS
		}
	}
	dev, err := device.Open(path,
		device.WithPixFormat(v4l2.PixFormat{
			PixelFormat: v4l2.PixelFmtMJPEG,
			Width:       uint32(w),
			Height:      uint32(h),
		}),
		device.WithFPS(uint32(math.Round(fps))),
		device.WithBufferSize(4),
	)
	if err != nil {
		return nil, fmt.Errorf("camera: open %s (%dx%d@%.4g): %w", path, w, h, fps, err)
	}
	skipped, err := applyControls(dev, cfg)
	if err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Start(ctx); err != nil {
		dev.Close()
		return nil, fmt.Errorf("camera: start stream: %w", err)
	}
	return &Camera{
		dev:     dev,
		frames:  dev.GetOutput(),
		skipped: skipped,
		mode:    mode{W: w, H: h, FPS: fps},
	}, nil
}

// probeBestMode opens the node briefly, enumerates every MJPEG geometry it
// advertises together with the rate each one sustains, and lets selectMode
// choose. The cap (config.MaxAuto{Width,Height}) keeps a >1080p camera from
// exceeding the Pi 5's software-decode budget (E-2 rev); it is applied inside
// selectMode so the rule lives in one place.
func probeBestMode(path string, targetFPS float64) (mode, error) {
	dev, err := device.Open(path)
	if err != nil {
		return mode{}, fmt.Errorf("open: %w", err)
	}
	defer dev.Close()
	sizes, err := v4l2.GetFormatFrameSizes(dev.Fd(), v4l2.PixelFmtMJPEG)
	if err != nil {
		return mode{}, fmt.Errorf("enum frame sizes: %w", err)
	}
	modes := make([]mode, 0, len(sizes))
	for _, s := range sizes {
		w, h := int(s.Size.MaxWidth), int(s.Size.MaxHeight)
		if w <= 0 || h <= 0 {
			continue
		}
		// A stepwise/continuous device reports a single entry covering a
		// range (GetFormatFrameSizes stops after index 0 for those), so the
		// range maximum is the best geometry we can name. UVC webcams are
		// discrete in practice.
		modes = append(modes, mode{W: w, H: h, FPS: maxFrameRate(dev.Fd(), uint32(w), uint32(h))})
	}
	return selectMode(modes, config.MaxAutoWidth, config.MaxAutoHeight, targetFPS)
}

// maxFrameRate returns the highest rate the device reports for one geometry,
// or 0 if it enumerates sizes but not intervals — selectMode treats that as
// "unknown" rather than "zero", so such a device still opens.
//
// V4L2 enumerates frame *intervals* (seconds per frame), so the rate is the
// inverted fraction and the shortest interval is the fastest mode. For a
// discrete device each index is one interval; for stepwise/continuous, Min is
// the shortest interval of the range.
func maxFrameRate(fd uintptr, w, h uint32) float64 {
	var best float64
	// Bounded: a driver that never returns the terminating error must not
	// hang the open. No real device enumerates anywhere near this many.
	for index := uint32(0); index < 64; index++ {
		fi, err := v4l2.GetFormatFrameInterval(fd, index, v4l2.PixelFmtMJPEG, w, h)
		if err != nil {
			return best
		}
		if r := rateOf(fi.Interval.Min); r > best {
			best = r
		}
		if fi.Type != v4l2.FrameIntervalTypeDiscrete {
			return best
		}
	}
	return best
}

func rateOf(f v4l2.Fract) float64 {
	if f.Numerator == 0 || f.Denominator == 0 {
		return 0
	}
	return float64(f.Denominator) / float64(f.Numerator)
}

// ctrlIDs maps the config-facing control names (controls.go) to their V4L2 ids.
var ctrlIDs = map[string]v4l2.CtrlID{
	ctrlFocusAuto:        v4l2.CtrlCameraFocusAuto,
	ctrlFocusAbsolute:    v4l2.CtrlCameraFocusAbsolute,
	ctrlExposureAuto:     v4l2.CtrlCameraExposureAuto,
	ctrlExposureAbsolute: v4l2.CtrlCameraExposureAbsolute,
}

// applyControls pins focus and exposure (FR-9; values from spike S-2) and
// returns the controls the device does not implement. Fixed-focus cameras —
// the class the appliance is built around (E-9) — expose no focus controls at
// all, so a missing control is skipped rather than fatal; see applyPlan.
func applyControls(dev *device.Device, cfg config.Config) ([]string, error) {
	return applyPlan(plannedControls(cfg), func(c control) error {
		id, ok := ctrlIDs[c.Name]
		if !ok {
			return fmt.Errorf("unknown control %q", c.Name)
		}
		err := v4l2.SetControlValue(dev.Fd(), id, v4l2.CtrlValue(c.Value))
		// go4vl discards the errno and reports its own values: an
		// unimplemented control id fails VIDIOC_QUERYCTRL with EINVAL
		// (ErrorBadArgument), a device with no control interface at all
		// answers ENOTTY (ErrorUnsupported). An out-of-range value is a
		// distinct, unwrapped go4vl error and correctly stays fatal.
		if err != nil && (errors.Is(err, v4l2.ErrorBadArgument) || errors.Is(err, v4l2.ErrorUnsupported)) {
			return fmt.Errorf("%w: %v", errUnsupportedControl, err)
		}
		return err
	})
}

// SkippedControls returns the configured controls this device does not
// implement, in application order. Empty on a camera that supports them all.
// Reported at startup so a control silently dropped from the config stays
// visible (NFR-8), and recorded by spike S-2.
func (c *Camera) SkippedControls() []string { return c.skipped }

// SelectedMode returns the geometry and frame rate actually opened, which on
// the "auto" profile is what the probe chose rather than the profile nominal.
// The pipeline reports and budgets against this (status fps/resolution, gap
// detection, clip export rate) — a clip encoded at the nominal 30 from a
// 60 fps capture would play at half speed (FR-5).
func (c *Camera) SelectedMode() (w, h int, fps float64) {
	return c.mode.W, c.mode.H, c.mode.FPS
}

// ReadFrame returns the next MJPEG frame, stamped with the wall clock
// (allowed here: hardware adapter, hard rule 6). The payload is copied out
// of go4vl's reused mmap buffer — Frame.JPEG must be immutable (hard rule 4).
func (c *Camera) ReadFrame(ctx context.Context) (frame.Frame, error) {
	select {
	case <-ctx.Done():
		return frame.Frame{}, ctx.Err()
	case b, ok := <-c.frames:
		if !ok {
			return frame.Frame{}, errors.New("camera: stream closed")
		}
		jpg := make([]byte, len(b))
		copy(jpg, b)
		seq := c.seq
		c.seq++
		return frame.Frame{Seq: seq, CaptureTS: time.Now(), JPEG: jpg}, nil
	}
}

// Close releases the device.
func (c *Camera) Close() error {
	return c.dev.Close()
}
