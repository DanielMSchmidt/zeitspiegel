//go:build !v4l2

package main

import (
	"context"
	"errors"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// openCamera without the v4l2 build tag: camera hardware is not compiled in
// (hard rule 2 — cgo only behind tags). Use --source synth.
func openCamera(_ context.Context, _ config.Config) (capture.Source, error) {
	return nil, errors.New("camera support not compiled in (build with -tags v4l2); use --source synth")
}
