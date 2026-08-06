//go:build integration

package export_test

// IT-3 / IT-4 / IT-9: real ffmpeg + ffprobe (TESTPLAN tier 2, `integration`
// tag). Exports are streamed fragmented MP4; tests capture the stream into a
// file before probing (ffprobe duration is only reliable on seekable input).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
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

// exportToFile streams a prepared export into a file and returns its path.
func exportToFile(t *testing.T, ex *export.Exporter, frames []frame.Frame, format export.Format) string {
	t.Helper()
	st, err := ex.Prepare(context.Background(), frames, fps, format)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(t.TempDir(), "clip.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := st.WriteTo(f); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return path
}

type probe struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		// empty_moov files carry no sample table, so nb_frames is empty;
		// -count_packets fills nb_read_packets instead (TESTPLAN IT-3).
		NBReadPackets string `json:"nb_read_packets"`
	} `json:"streams"`
}

func ffprobe(t *testing.T, path string) probe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_packets",
		"-show_entries",
		"format=format_name,duration:stream=codec_name,nb_read_packets",
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

// IT-3: /clip semantics at the export layer — valid fragmented MP4, duration
// 10 s ± 1 frame, 600 ± 1 packets.
func TestExportMP4(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(3)
	path := exportToFile(t, ex, w.Frames, export.FormatMP4)

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
	n, err := strconv.Atoi(p.Streams[0].NBReadPackets)
	if err != nil {
		t.Fatal(err)
	}
	if n < 600 || n > 601 {
		t.Errorf("packets = %d, want 600 ± 1", n)
	}
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
	ex := export.New(3)
	path := exportToFile(t, ex, w.Frames, export.FormatMP4)

	wantFirst, wantLast := w.Frames[0].Seq, w.Frames[len(w.Frames)-1].Seq
	if got := synth.DecodeSeqPixels(extractPNG(t, path, 0)); got != wantFirst {
		t.Errorf("first frame seq = %d, want %d", got, wantFirst)
	}
	if got := synth.DecodeSeqPixels(extractPNG(t, path, len(w.Frames)-1)); got != wantLast {
		t.Errorf("last frame seq = %d, want %d", got, wantLast)
	}
}

// IT-4 (MJPEG copy path): stream copy preserves the original JPEG bytes, so
// the APP4 tags survive into the demuxed frames — this is also the proof
// that mjpeg-in-fragmented-MP4 muxes and demuxes cleanly.
func TestExportMJPEGCopyFrameAccurate(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(3)
	path := exportToFile(t, ex, w.Frames, export.FormatMJPEG)

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
	ex := export.New(1)
	release, err := ex.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := ex.Prepare(context.Background(), w.Frames, fps, export.FormatMP4); err != export.ErrBusy {
		t.Errorf("err = %v, want ErrBusy", err)
	}
}

// Close without WriteTo releases the slot (the handler's defer path when it
// bails before streaming).
func TestExportCloseReleasesSlot(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(1)
	st, err := ex.Prepare(context.Background(), w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	release, err := ex.Acquire()
	if err != nil {
		t.Fatalf("slot not released after Close: %v", err)
	}
	release()
}

// IT-9: the container header reaches the consumer while the encode is still
// running — the whole point of streaming. A buffer-then-dump implementation
// would deliver the first byte only at the very end.
func TestExportStreamsBeforeCompletion(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(1)
	st, err := ex.Prepare(context.Background(), w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pr, pw := io.Pipe()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := st.WriteTo(pw)
		pw.CloseWithError(err)
		done <- err
	}()

	head := make([]byte, 8)
	if _, err := io.ReadFull(pr, head); err != nil {
		t.Fatalf("reading stream head: %v", err)
	}
	tFirst := time.Since(start)
	if got := string(head[4:8]); got != "ftyp" {
		t.Errorf("stream head box = %q, want ftyp", got)
	}
	select {
	case err := <-done:
		t.Fatalf("WriteTo returned before the body was drained (err=%v)", err)
	default:
	}

	if _, err := io.Copy(io.Discard, pr); err != nil {
		t.Fatalf("draining stream: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	total := time.Since(start)
	if tFirst > total/2 && tFirst > 500*time.Millisecond {
		t.Errorf("first byte after %v of %v total — output is not streamed", tFirst, total)
	}
}

// IT-9: cancelling the request context (client disconnect) kills ffmpeg,
// unblocks WriteTo promptly, and frees the export slot.
func TestExportAbortKillsFFmpeg(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := ex.Prepare(ctx, w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := st.WriteTo(pw)
		pw.CloseWithError(err)
		done <- err
	}()
	go io.Copy(io.Discard, pr) // keep the consumer draining, like a browser

	head := make([]byte, 8)
	if _, err := io.ReadFull(pr, head); err != nil {
		t.Fatalf("reading stream head: %v", err)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WriteTo did not return within 2 s of cancellation")
	}
	release, err := ex.Acquire()
	if err != nil {
		t.Fatalf("slot not released after abort: %v", err)
	}
	release()
}

// IT-9 (writer failure flavor): a failing destination — the HTTP write
// deadline path — aborts the encode and surfaces the write error.
func TestExportWriterErrorAborts(t *testing.T) {
	w := cutLast10s(t, fillBuffer(t))
	ex := export.New(1)
	st, err := ex.Prepare(context.Background(), w.Frames, fps, export.FormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	errBoom := errors.New("boom")
	done := make(chan error, 1)
	go func() {
		_, err := st.WriteTo(&failAfter{n: 4096, err: errBoom})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errBoom) {
			t.Errorf("err = %v, want errBoom", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteTo did not return after writer failure")
	}
	release, err := ex.Acquire()
	if err != nil {
		t.Fatalf("slot not released after writer failure: %v", err)
	}
	release()
}

// failAfter accepts n bytes, then fails every Write with err.
type failAfter struct {
	n    int
	got  int
	err  error
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.got >= f.n {
		return 0, f.err
	}
	f.got += len(p)
	return len(p), nil
}
