//go:build integration

package export_test

// IT-3 / IT-4: real ffmpeg + ffprobe (TESTPLAN tier 2, `integration` tag).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/export"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
	"github.com/danielmschmidt/zeitspiegel/internal/window"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

const fps = 60

var (
	fillOnce  sync.Once
	sharedBuf *ringbuf.Buffer
)

// fillBuffer returns a buffer with 12 s of synthetic frames, generated once
// and shared read-only across the tests in this package.
func fillBuffer(t *testing.T) *ringbuf.Buffer {
	t.Helper()
	fillOnce.Do(func() {
		src := synth.NewSource(fps, t0)
		sharedBuf = ringbuf.New(time.Hour, 1<<31)
		for i := 0; i < 12*fps+1; i++ {
			sharedBuf.Push(src.Next())
		}
	})
	return sharedBuf
}

func cutLast10s(t *testing.T, b *ringbuf.Buffer) window.Window {
	t.Helper()
	newest, err := b.Newest()
	if err != nil {
		t.Fatal(err)
	}
	w, err := window.Cut(b, newest.CaptureTS, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type probe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		NBFrames  string `json:"nb_frames"`
	} `json:"streams"`
}

func ffprobe(t *testing.T, path string) probe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"format=format_name,duration:stream=codec_name,nb_frames",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var p probe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("ffprobe json: %v", err)
	}
	return p
}

// IT-3: /clip semantics at the export layer — valid MP4, duration 10 s ± 1
// frame, 600 ± 1 frames.
func TestExportMP4(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(t.TempDir(), 3)
	path, cleanup, err := ex.Export(context.Background(), w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	p := ffprobe(t, path)
	if p.Format.FormatName != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("format = %q, want mp4 family", p.Format.FormatName)
	}
	if len(p.Streams) != 1 || p.Streams[0].CodecName != "h264" {
		t.Fatalf("streams = %+v, want one h264 stream", p.Streams)
	}
	dur, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		t.Fatal(err)
	}
	if d := dur - 10.0; d < -1.0/fps || d > 2.0/fps {
		t.Errorf("duration = %v s, want 10 ± 1 frame", dur)
	}
	n, err := strconv.Atoi(p.Streams[0].NBFrames)
	if err != nil {
		t.Fatal(err)
	}
	if n < 600 || n > 601 {
		t.Errorf("frames = %d, want 600 ± 1", n)
	}
	if cleanup(); fileExists(path) {
		t.Errorf("cleanup left %s behind", path)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractPNG pulls video frame index n out of the clip.
func extractPNG(t *testing.T, clip string, n int) image.Image {
	t.Helper()
	out := filepath.Join(t.TempDir(), "f.png")
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", clip,
		"-vf", fmt.Sprintf("select=eq(n\\,%d)", n), "-frames:v", "1", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg extract: %v\n%s", err, stderr.String())
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// IT-4 (H.264 path): first and last clip frames carry the expected seq
// numbers, read back from the lossy-codec-safe pixel pattern.
func TestExportMP4FrameAccurate(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(t.TempDir(), 3)
	path, cleanup, err := ex.Export(context.Background(), w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	wantFirst, wantLast := w.Frames[0].Seq, w.Frames[len(w.Frames)-1].Seq
	if got := synth.DecodeSeqPixels(extractPNG(t, path, 0)); got != wantFirst {
		t.Errorf("first frame seq = %d, want %d", got, wantFirst)
	}
	if got := synth.DecodeSeqPixels(extractPNG(t, path, len(w.Frames)-1)); got != wantLast {
		t.Errorf("last frame seq = %d, want %d", got, wantLast)
	}
}

// IT-4 (MJPEG copy path): stream copy preserves the original JPEG bytes, so
// the APP4 tags survive into the demuxed frames.
func TestExportMJPEGCopyFrameAccurate(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(t.TempDir(), 3)
	path, cleanup, err := ex.Export(context.Background(), w.Frames, fps, export.FormatMJPEG)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	p := ffprobe(t, path)
	if len(p.Streams) != 1 || p.Streams[0].CodecName != "mjpeg" {
		t.Fatalf("streams = %+v, want one mjpeg stream", p.Streams)
	}

	// demux without re-encode; frame JPEGs come out byte-identical
	dir := t.TempDir()
	if err := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-c", "copy", filepath.Join(dir, "%06d.jpg")).Run(); err != nil {
		t.Fatalf("ffmpeg demux: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jpg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no demuxed frames (err=%v)", err)
	}
	checkSeq := func(file string, want uint64) {
		jpg, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		seq, _, err := frame.ParseAPP4(jpg)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if seq != want {
			t.Errorf("%s: seq = %d, want %d", filepath.Base(file), seq, want)
		}
	}
	checkSeq(files[0], w.Frames[0].Seq)
	checkSeq(files[len(files)-1], w.Frames[len(w.Frames)-1].Seq)
}

// All slots taken ⇒ ErrBusy immediately (maps to 503 + Retry-After).
func TestExportSlotsBusy(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(t.TempDir(), 1)
	release, err := ex.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, err := ex.Export(context.Background(), w.Frames, fps, export.FormatMP4); err != export.ErrBusy {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}
