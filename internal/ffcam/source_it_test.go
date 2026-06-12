//go:build integration

package ffcam_test

// Subprocess plumbing against real ffmpeg, with lavfi testsrc standing in
// for a camera (no hardware in CI).

import (
	"bytes"
	"context"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/ffcam"
)

var _ capture.Source = (*ffcam.Source)(nil)

func TestSourceReadsFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := ffcam.Open(ctx, ffcam.Options{
		InputArgs: []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30"},
		OutputFPS: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	var lastSeq uint64
	var lastTS time.Time
	for i := 0; i < 10; i++ {
		f, err := src.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Seq != uint64(i) {
			t.Fatalf("frame %d: seq = %d", i, f.Seq)
		}
		if i > 0 && f.CaptureTS.Before(lastTS) {
			t.Fatalf("timestamps went backwards")
		}
		if _, err := jpeg.Decode(bytes.NewReader(f.JPEG)); err != nil {
			t.Fatalf("frame %d: not a decodable JPEG: %v", i, err)
		}
		lastSeq, lastTS = f.Seq, f.CaptureTS
	}
	_ = lastSeq

	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := src.ReadFrame(ctx); err == nil {
		t.Error("ReadFrame after Close must fail (supervisor reopens)")
	}
}

func TestSourceReportsFFmpegFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	src, err := ffcam.Open(ctx, ffcam.Options{
		InputArgs: []string{"-f", "lavfi", "-i", "nosuchsrc=broken"},
		OutputFPS: 30,
	})
	if err != nil {
		return // failing at Open with a useful error is fine too
	}
	defer src.Close()
	_, err = src.ReadFrame(ctx)
	if err == nil {
		t.Fatal("want error from broken input")
	}
	if !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("error %q lacks ffmpeg context for diagnosis", err)
	}
}

func TestSourceCtxCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	src, err := ffcam.Open(ctx, ffcam.Options{
		InputArgs: []string{"-f", "lavfi", "-re", "-i", "testsrc=size=320x240:rate=5"},
		OutputFPS: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rctx, rcancel := context.WithCancel(ctx)
	rcancel()
	if _, err := src.ReadFrame(rctx); err == nil {
		t.Error("canceled ctx must abort ReadFrame")
	}
}
