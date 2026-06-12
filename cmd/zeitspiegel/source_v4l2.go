//go:build v4l2

package main

import (
	"context"

	"github.com/danielmschmidt/zeitspiegel/internal/camera"
	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

// openCamera opens the UVC device via the go4vl adapter.
func openCamera(ctx context.Context, cfg config.Config) (capture.Source, error) {
	return camera.Open(ctx, cfg)
}
