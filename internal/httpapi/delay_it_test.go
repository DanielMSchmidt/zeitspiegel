package httpapi_test

// IT-2 / FR-3: a delay change over real HTTP is effective within one frame
// interval. Pure: synth source + FakeClock + httptest.

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

func TestDelayChangeEffectiveWithinOneFrame(t *testing.T) {
	const fps = 60
	interval := time.Second / fps
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	src := synth.NewSource(fps, t0)
	buf := ringbuf.New(time.Minute, 1<<30)
	e := engine.New(buf)
	e.SetDelay(2 * time.Second)
	clk := synth.NewFakeClock(t0)

	deps := httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: &fakeStatus{st: defaultStatus()},
		Delay:  e,
		Clip:   &fakeClip{},
		Config: &fakeStore{r: config.Default().Runtime()},
	}
	srv := httptest.NewServer(httpapi.New(deps))
	defer srv.Close()

	// run 4 s of pipeline at delay 2 s
	for i := 0; i < 4*fps; i++ {
		buf.Push(src.Next())
		e.Tick(clk.Now())
		clk.Advance(interval)
	}

	// change delay to 1 s over real HTTP
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/delay", bytes.NewReader([]byte(`{"seconds": 1.0}`)))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT /delay: %d", resp.StatusCode)
	}

	// the very next tick must select at now − 1 s (≤ 1 frame interval after the 200)
	buf.Push(src.Next())
	sel := e.Tick(clk.Now())
	if !sel.Render {
		t.Fatal("next tick after delay change must render (hard cut)")
	}
	lag := clk.Now().Sub(sel.Frame.CaptureTS)
	if d := lag - time.Second; d < -interval || d > interval {
		t.Fatalf("lag after change = %v, want 1 s ± %v", lag, interval)
	}
}
