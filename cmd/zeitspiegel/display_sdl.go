//go:build sdl

package main

import (
	"runtime"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/screen"
)

// SDL requires the render loop on the main OS thread (ARCHITECTURE §4).
func init() {
	runtime.LockOSThread()
}

// openDisplay brings up the full-screen KMSDRM renderer.
func openDisplay(cfg config.Config) (engine.Display, func() error, error) {
	s, err := screen.Open(cfg.MirrorFlip)
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}

// displayMirrorFunc exposes the runtime mirror toggle (PATCH /config).
func displayMirrorFunc(d engine.Display) func(bool) {
	if s, ok := d.(*screen.Screen); ok {
		return s.SetMirror
	}
	return nil
}
