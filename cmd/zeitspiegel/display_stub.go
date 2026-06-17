//go:build !sdl

package main

import (
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
)

// openDisplay without the sdl build tag: headless mode. The delayed image is
// still observable via GET /api/v1/preview (demo mode on any machine).
func openDisplay(_ config.Config, _ bool) (engine.Display, func() error, error) {
	return nil, nil, nil
}

// displayMirrorFunc has nothing to control headless.
func displayMirrorFunc(engine.Display) func(bool) { return nil }

// displayEvents: no window, no events.
func displayEvents(engine.Display) func() bool { return nil }

// displayDelayFunc: no badge in headless mode.
func displayDelayFunc(engine.Display) func(time.Duration) { return nil }
