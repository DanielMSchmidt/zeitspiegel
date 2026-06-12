//go:build v4l2

// Package camera is the thin V4L2 adapter over go4vl (ARCHITECTURE §3).
// cgo lives only here and in internal/screen (hard rule 2); core packages
// never import this package.
package camera

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vladimirvivien/go4vl/device"
	"github.com/vladimirvivien/go4vl/v4l2"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Camera implements capture.Source over a UVC device in native MJPEG (D2).
type Camera struct {
	dev    *device.Device
	frames <-chan []byte
	seq    uint64
}

// Open configures and starts the device per the boot config (FR-9: pinned
// focus / exposure to prevent hunting during movement).
func Open(ctx context.Context, cfg config.Config) (*Camera, error) {
	w, h := cfg.Resolution()
	dev, err := device.Open(cfg.Device,
		device.WithPixFormat(v4l2.PixFormat{
			PixelFormat: v4l2.PixelFmtMJPEG,
			Width:       uint32(w),
			Height:      uint32(h),
		}),
		device.WithFPS(uint32(cfg.FPS())),
		device.WithBufferSize(4),
	)
	if err != nil {
		return nil, fmt.Errorf("camera: open %s: %w", cfg.Device, err)
	}
	if err := applyControls(dev, cfg); err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.Start(ctx); err != nil {
		dev.Close()
		return nil, fmt.Errorf("camera: start stream: %w", err)
	}
	return &Camera{dev: dev, frames: dev.GetOutput()}, nil
}

// applyControls pins focus and exposure (FR-9; values from spike S-2).
// UVC: exposure_auto menu is 1 = manual, 3 = aperture priority (auto).
func applyControls(dev *device.Device, cfg config.Config) error {
	set := func(id v4l2.CtrlID, val v4l2.CtrlValue, name string) error {
		if err := v4l2.SetControlValue(dev.Fd(), id, val); err != nil {
			return fmt.Errorf("camera: set %s=%d: %w", name, val, err)
		}
		return nil
	}
	focusAuto := v4l2.CtrlValue(0)
	if cfg.FocusAuto {
		focusAuto = 1
	}
	if err := set(v4l2.CtrlCameraFocusAuto, focusAuto, "focus_auto"); err != nil {
		return err
	}
	if !cfg.FocusAuto {
		if err := set(v4l2.CtrlCameraFocusAbsolute, v4l2.CtrlValue(cfg.FocusAbsolute), "focus_absolute"); err != nil {
			return err
		}
	}
	expoAuto := v4l2.CtrlValue(1) // manual
	if cfg.ExposureAuto {
		expoAuto = 3 // aperture priority
	}
	if err := set(v4l2.CtrlCameraExposureAuto, expoAuto, "exposure_auto"); err != nil {
		return err
	}
	if !cfg.ExposureAuto {
		if err := set(v4l2.CtrlCameraExposureAbsolute, v4l2.CtrlValue(cfg.ExposureAbsolute), "exposure_absolute"); err != nil {
			return err
		}
	}
	return nil
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
