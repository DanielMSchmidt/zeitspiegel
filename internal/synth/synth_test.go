package synth_test

// TESTPLAN step 2: the synthetic source / fake clock / fake display are test
// infrastructure and are themselves tested here.

import (
	"bytes"
	"image/jpeg"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

var start = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestFakeClock(t *testing.T) {
	c := synth.NewFakeClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now = %v, want %v", c.Now(), start)
	}
	c.Advance(1500 * time.Millisecond)
	if want := start.Add(1500 * time.Millisecond); !c.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", c.Now(), want)
	}
}

func TestSourceExactRate(t *testing.T) {
	s := synth.NewSource(60, start) // 60 fps
	var prev frame.Frame
	for i := 0; i < 10; i++ {
		f := s.Next()
		if f.Seq != uint64(i) {
			t.Fatalf("frame %d: seq = %d", i, f.Seq)
		}
		want := start.Add(time.Duration(i) * time.Second / 60)
		if !f.CaptureTS.Equal(want) {
			t.Fatalf("frame %d: ts = %v, want %v (exact interval)", i, f.CaptureTS, want)
		}
		if i > 0 && !f.CaptureTS.After(prev.CaptureTS) {
			t.Fatalf("timestamps not strictly increasing")
		}
		prev = f
	}
}

func TestSourceFrameCarriesSeqAndTS(t *testing.T) {
	s := synth.NewSource(30, start)
	s.Next()
	f := s.Next() // seq 1
	seq, ts, err := frame.ParseAPP4(f.JPEG)
	if err != nil {
		t.Fatalf("ParseAPP4: %v", err)
	}
	if seq != f.Seq || !ts.Equal(f.CaptureTS) {
		t.Errorf("APP4 (%d, %v) != frame (%d, %v)", seq, ts, f.Seq, f.CaptureTS)
	}

	img, err := jpeg.Decode(bytes.NewReader(f.JPEG))
	if err != nil {
		t.Fatalf("frame JPEG does not decode: %v", err)
	}
	if got := synth.DecodeSeqPixels(img); got != f.Seq {
		t.Errorf("pixel pattern decodes to %d, want %d", got, f.Seq)
	}
}

// The pixel pattern must survive lossy re-encoding (clip exports are H.264,
// IT-4 reads seq back from decoded video frames).
func TestSeqPixelsSurviveLossyRecode(t *testing.T) {
	s := synth.NewSource(60, start)
	s.SetSeq(0xDEADBEEF) // a seq with many bits set, without generating 3.7M frames
	f := s.Next()
	if f.Seq != 0xDEADBEEF {
		t.Fatalf("seq = %#x, want 0xDEADBEEF", f.Seq)
	}
	img, err := jpeg.Decode(bytes.NewReader(f.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	var lossy bytes.Buffer
	if err := jpeg.Encode(&lossy, img, &jpeg.Options{Quality: 30}); err != nil {
		t.Fatal(err)
	}
	img2, err := jpeg.Decode(&lossy)
	if err != nil {
		t.Fatal(err)
	}
	if got := synth.DecodeSeqPixels(img2); got != f.Seq {
		t.Errorf("after q30 recode pixel pattern decodes to %d, want %d", got, f.Seq)
	}
}

func TestFakeDisplayRecords(t *testing.T) {
	c := synth.NewFakeClock(start)
	d := synth.NewFakeDisplay(c)
	s := synth.NewSource(60, start)
	f0, f1 := s.Next(), s.Next()

	if err := d.Render(f0); err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Second / 60)
	if err := d.Render(f1); err != nil {
		t.Fatal(err)
	}

	recs := d.Records()
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].Frame.Seq != 0 || !recs[0].RenderedAt.Equal(start) {
		t.Errorf("record 0 = (%d, %v), want (0, %v)", recs[0].Frame.Seq, recs[0].RenderedAt, start)
	}
	if want := start.Add(time.Second / 60); recs[1].Frame.Seq != 1 || !recs[1].RenderedAt.Equal(want) {
		t.Errorf("record 1 = (%d, %v), want (1, %v)", recs[1].Frame.Seq, recs[1].RenderedAt, want)
	}
}
