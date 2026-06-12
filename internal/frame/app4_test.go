package frame_test

// Test infra for TESTPLAN step 2: APP4 seq/timestamp tagging used by
// synth.Source and the frame-accuracy assertions (IT-1, IT-4).

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

func plainJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(2, 2, color.Black)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAPP4RoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 13, 10, 0, 0, 123456789, time.UTC)
	tagged, err := frame.TagAPP4(plainJPEG(t), 42, ts)
	if err != nil {
		t.Fatalf("TagAPP4: %v", err)
	}
	seq, got, err := frame.ParseAPP4(tagged)
	if err != nil {
		t.Fatalf("ParseAPP4: %v", err)
	}
	if seq != 42 {
		t.Errorf("seq = %d, want 42", seq)
	}
	if !got.Equal(ts) {
		t.Errorf("ts = %v, want %v", got, ts)
	}
	// the tagged bytes must remain a decodable JPEG
	if _, err := jpeg.Decode(bytes.NewReader(tagged)); err != nil {
		t.Errorf("tagged JPEG no longer decodes: %v", err)
	}
}

func TestParseAPP4Missing(t *testing.T) {
	if _, _, err := frame.ParseAPP4(plainJPEG(t)); err == nil {
		t.Error("want error for JPEG without APP4 tag")
	}
	if _, _, err := frame.ParseAPP4([]byte{0x00, 0x01}); err == nil {
		t.Error("want error for non-JPEG bytes")
	}
}
