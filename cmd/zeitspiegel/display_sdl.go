//go:build sdl

package main

import (
	"runtime"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/screen"
)

// SDL requires the render loop on the main OS thread (ARCHITECTURE §4).
func init() {
	runtime.LockOSThread()
}

// openDisplay brings up the renderer: full-screen on the appliance,
// a desktop window with --windowed (the dev TV view).
func openDisplay(cfg config.Config, windowed bool) (engine.Display, func() error, error) {
	w, h := cfg.Resolution()
	s, err := screen.Open(screen.Options{
		Mirror: cfg.MirrorFlip, Windowed: windowed, Width: w, Height: h,
	})
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}

// displayEvents pumps the SDL event queue each tick; true = user closed the
// dev window.
func displayEvents(d engine.Display) func() bool {
	if s, ok := d.(*screen.Screen); ok {
		return s.ProcessEvents
	}
	return nil
}

// displayMirrorFunc exposes the runtime mirror toggle (PATCH /config).
func displayMirrorFunc(d engine.Display) func(bool) {
	if s, ok := d.(*screen.Screen); ok {
		return s.SetMirror
	}
	return nil
}

// displayDelayFunc exposes the badge delay setter used by the render loop.
func displayDelayFunc(d engine.Display) func(time.Duration) {
	if s, ok := d.(*screen.Screen); ok {
		return s.SetDelay
	}
	return nil
}

// displaySplashFunc returns the paint-the-splash closure (nil headless).
// The render loop calls it while warming up so the screen isn't black
// between SDL open and the first camera frame.
func displaySplashFunc(d engine.Display) func() error {
	if s, ok := d.(*screen.Screen); ok {
		return s.Splash
	}
	return nil
}
