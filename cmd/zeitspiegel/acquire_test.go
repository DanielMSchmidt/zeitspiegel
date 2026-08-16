package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// nullDisplay stands in for a screen that opened.
type nullDisplay struct{}

func (nullDisplay) Render(frame.Frame) error { return nil }

// openStub scripts what openDisplay returns, call by call.
type openStub struct {
	results []error // one per call; nil = success
	calls   int
	closed  int
}

func (o *openStub) open() (engine.Display, func() error, error) {
	i := o.calls
	o.calls++
	if i < len(o.results) && o.results[i] != nil {
		return nil, nil, o.results[i]
	}
	return nullDisplay{}, func() error { o.closed++; return nil }, nil
}

func newAcquirer(open func() (engine.Display, func() error, error), buf *bytes.Buffer) *displayAcquirer {
	return &displayAcquirer{
		open:   open,
		every:  2 * time.Second,
		logger: slog.New(slog.NewTextHandler(buf, nil)),
	}
}

var acqT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// UT-43: a unit whose screen is plugged in gets it on the first tick. The
// retry interval must not become a delay on the path that always worked.
func TestAcquirerOpensImmediately(t *testing.T) {
	stub := &openStub{}
	a := newAcquirer(stub.open, &bytes.Buffer{})

	d, closeFn := a.try(acqT0)
	if d == nil {
		t.Fatal("no display on the first attempt, want one")
	}
	if closeFn == nil {
		t.Fatal("no close func alongside the display")
	}
	if stub.calls != 1 {
		t.Fatalf("open called %d times, want 1", stub.calls)
	}
	if !a.status()["open"].(bool) {
		t.Error("status reports the display closed after a successful open")
	}
}

// UT-43: the field failure. No HDMI attached ⇒ SDL cannot init ⇒ the unit
// used to exit and systemd restarted it forever, with no web UI and no radio
// the whole time. Now it simply has no display yet, and keeps asking.
func TestAcquirerRetriesOnBoundedInterval(t *testing.T) {
	stub := &openStub{results: []error{
		errors.New("screen: sdl init: kmsdrm not available"),
		errors.New("screen: sdl init: kmsdrm not available"),
	}}
	a := newAcquirer(stub.open, &bytes.Buffer{})

	if d, _ := a.try(acqT0); d != nil {
		t.Fatal("got a display from a failing open")
	}
	// The render loop ticks at 60 Hz; retrying SDL init on every one of them
	// would spend the whole tick budget on failing to open a screen.
	for i := 1; i <= 60; i++ {
		a.try(acqT0.Add(time.Duration(i) * 16 * time.Millisecond))
	}
	if stub.calls != 1 {
		t.Fatalf("open called %d times within the retry interval, want 1", stub.calls)
	}

	// Second attempt, still no cable.
	if d, _ := a.try(acqT0.Add(2 * time.Second)); d != nil {
		t.Fatal("got a display from a failing open")
	}
	if stub.calls != 2 {
		t.Fatalf("open called %d times, want 2", stub.calls)
	}

	// Cable arrives: the next attempt succeeds and the retries stop.
	d, _ := a.try(acqT0.Add(4 * time.Second))
	if d == nil {
		t.Fatal("no display once open succeeds")
	}
	a.try(acqT0.Add(10 * time.Second))
	if stub.calls != 3 {
		t.Fatalf("open called %d times, want 3 — an acquired display is not reopened", stub.calls)
	}
}

// UT-43: "display opened" is one of the startup milestones
// deploy/zeitspiegel-boot-profile.sh greps out of the journal to build a
// card's boot timing line-up (NFR-8, FR-12). Moving the open into the render
// loop must not cost that line — including on the ordinary path where the
// screen was there all along and the first attempt succeeds.
func TestAcquirerLogsTheOpenMilestone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []error
	}{
		{"first attempt", nil},
		{"after waiting for a cable", []error{errors.New("kmsdrm not available")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			stub := &openStub{results: tc.results}
			a := newAcquirer(stub.open, &buf)
			now := acqT0
			a.now = func() time.Time { now = now.Add(300 * time.Millisecond); return now }

			for i := 0; i <= len(tc.results); i++ {
				a.try(acqT0.Add(time.Duration(i) * 2 * time.Second))
			}
			if !strings.Contains(buf.String(), `msg="display opened"`) {
				t.Fatalf("no display-opened milestone in the journal:\n%s", buf.String())
			}
			if !strings.Contains(buf.String(), "took=") {
				t.Errorf("the milestone carries no timing:\n%s", buf.String())
			}
		})
	}
}

// UT-43: the journal has to explain a dark unit, but the retry runs for as
// long as the unit is powered — so a repeated failure is logged once, and a
// changed one is logged again (NFR-8).
func TestAcquirerLogsEachDistinctFailureOnce(t *testing.T) {
	var buf bytes.Buffer
	stub := &openStub{results: []error{
		errors.New("kmsdrm not available"),
		errors.New("kmsdrm not available"),
		errors.New("no such device"),
	}}
	a := newAcquirer(stub.open, &buf)

	a.try(acqT0)
	a.try(acqT0.Add(2 * time.Second))
	a.try(acqT0.Add(4 * time.Second))

	if got := strings.Count(buf.String(), "kmsdrm not available"); got != 1 {
		t.Errorf("the same failure was logged %d times, want 1", got)
	}
	if got := strings.Count(buf.String(), "no such device"); got != 1 {
		t.Errorf("a changed failure was logged %d times, want 1", got)
	}
}

// UT-43: what /debug/vars says while a unit is running without a screen —
// the one place a card pulled from a venue can be asked why it showed
// nothing (NFR-8).
func TestAcquirerStatusReportsWhyThereIsNoDisplay(t *testing.T) {
	stub := &openStub{results: []error{errors.New("kmsdrm not available")}}
	a := newAcquirer(stub.open, &bytes.Buffer{})

	a.try(acqT0)
	st := a.status()
	if st["open"].(bool) {
		t.Error("status reports a display that never opened")
	}
	if st["attempts"].(uint64) != 1 {
		t.Errorf("attempts = %v, want 1", st["attempts"])
	}
	if !strings.Contains(st["last_error"].(string), "kmsdrm") {
		t.Errorf("last_error = %q, want the open failure", st["last_error"])
	}
}

// UT-43: a build with no display support at all (no sdl tag — the demo and
// CI path) reports no display and no error, and must not spend the rest of
// the process retrying something that can never succeed.
func TestAcquirerStopsOnAHeadlessBuild(t *testing.T) {
	calls := 0
	a := newAcquirer(func() (engine.Display, func() error, error) {
		calls++
		return nil, nil, nil
	}, &bytes.Buffer{})

	if d, _ := a.try(acqT0); d != nil {
		t.Fatal("headless build handed back a display")
	}
	a.try(acqT0.Add(10 * time.Second))
	a.try(acqT0.Add(20 * time.Second))
	if calls != 1 {
		t.Fatalf("open called %d times on a headless build, want 1", calls)
	}
	if st := a.status(); st["open"].(bool) || st["headless"] != true {
		t.Errorf("status = %v, want headless", st)
	}
}

// UT-43: a display that arrives late arrives into a unit that has been
// running, and may have been reconfigured while it had no screen — the
// control page works from the moment the unit boots now, so a mirror flip
// can land before there is anything to flip. The screen picks up the value
// in force, not the one the config file booted with.
func TestAttachDisplayAppliesTheCurrentMirrorFlip(t *testing.T) {
	cfg := config.Default()
	cfg.MirrorFlip = true
	store := &runtimeStore{rt: cfg.Runtime(), buf: ringbuf.New(time.Minute, 1<<20), restart: &atomic.Bool{}}

	// No screen yet: the guest turns the mirroring off.
	if _, err := store.Apply(config.Patch{MirrorFlip: boolPtr(false)}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var got []bool
	store.attachMirror(func(on bool) { got = append(got, on) })
	if len(got) != 1 || got[0] {
		t.Fatalf("mirror calls on attach = %v, want exactly [false] — the value in force", got)
	}

	// And it stays wired: later changes reach the screen as they always did.
	if _, err := store.Apply(config.Patch{MirrorFlip: boolPtr(true)}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(got) != 2 || !got[1] {
		t.Fatalf("mirror calls after a patch = %v, want [false true]", got)
	}
}

func boolPtr(b bool) *bool { return &b }
