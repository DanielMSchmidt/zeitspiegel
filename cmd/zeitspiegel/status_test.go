package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/capture"
	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/frame"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
)

// FR-8/FR-10: /status warming_up must follow the engine's semantics — an
// empty buffer is warming up, and so is a delay target before the oldest
// frame; a satisfiable target is not.
func TestStatusWarmingUpMatchesEngine(t *testing.T) {
	newStatus := func(buf *ringbuf.Buffer, delay time.Duration) *sysStatus {
		cfg := config.Default()
		eng := engine.New(buf)
		eng.SetDelay(delay)
		return &sysStatus{
			start: time.Now(),
			cfg:   cfg,
			store: &runtimeStore{rt: cfg.Runtime(), buf: buf, restart: &atomic.Bool{}},
			buf:   buf,
			eng:   eng,
			sup:   capture.New(capture.Options{}),
		}
	}

	empty := ringbuf.New(time.Minute, 1<<20)
	if st := newStatus(empty, 0).Status(); !st.WarmingUp {
		t.Error("empty buffer with delay 0: warming_up = false, want true (nothing to display)")
	}

	filled := ringbuf.New(time.Minute, 1<<20)
	filled.Push(frame.Frame{Seq: 0, CaptureTS: time.Now().Add(-time.Second)})
	filled.Push(frame.Frame{Seq: 1, CaptureTS: time.Now()})
	if st := newStatus(filled, 0).Status(); st.WarmingUp {
		t.Error("satisfiable delay: warming_up = true, want false")
	}
	if st := newStatus(filled, time.Hour).Status(); !st.WarmingUp {
		t.Error("delay beyond oldest frame: warming_up = false, want true")
	}
}

// UT-46: /status carries the runtime config, so a page watching a unit needs
// one poll rather than two. It is the same snapshot the rest of the body was
// computed from — a status reporting a capacity from one read of the config
// and a mirror flip from another would be a body describing no moment that
// ever existed.
func TestStatusCarriesRuntimeConfig(t *testing.T) {
	buf := ringbuf.New(time.Minute, 1<<20)
	cfg := config.Default()
	cfg.MirrorFlip = true
	cfg.BufferMaxS = 42
	store := &runtimeStore{rt: cfg.Runtime(), buf: buf, restart: &atomic.Bool{}}
	s := &sysStatus{
		start: time.Now(),
		cfg:   cfg,
		store: store,
		buf:   buf,
		eng:   engine.New(buf),
		sup:   capture.New(capture.Options{}),
	}

	st := s.Status()
	if st.Config != cfg.Runtime() {
		t.Errorf("status config = %+v, want %+v", st.Config, cfg.Runtime())
	}
	if st.Buffer.CapacityS != st.Config.BufferMaxS {
		t.Errorf("capacity_s = %v but config.buffer_max_s = %v: two reads of one config",
			st.Buffer.CapacityS, st.Config.BufferMaxS)
	}

	// The shape is pinned by REQUIREMENTS §3: a nested `config` object
	// carrying what GET /api/v1/config answers with.
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Config *config.Runtime `json:"config"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	if body.Config == nil {
		t.Fatalf("no `config` object in /status: %s", b)
	}
	if !body.Config.MirrorFlip {
		t.Errorf("config.mirror_flip = false, want true")
	}
}

// UT-48 (FR-18): what the store accepts, the store hands to be written down.
// The appliance is switched off by pulling its plug, so a setting that lives
// only in this process is a setting the guest gets to change again tomorrow.
func TestRuntimeStorePersistsWhatItAccepted(t *testing.T) {
	buf := ringbuf.New(time.Minute, 1<<20)
	cfg := config.Default()
	cfg.MirrorFlip = false

	var saved []config.Patch
	store := &runtimeStore{
		rt: cfg.Runtime(), buf: buf, restart: &atomic.Bool{},
		save: func(p config.Patch) { saved = append(saved, p) },
	}

	if _, err := store.Apply(config.Patch{MirrorFlip: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].MirrorFlip == nil || !*saved[0].MirrorFlip {
		t.Fatalf("after a flip: saved = %+v, want one patch turning the flip on", saved)
	}

	// Patches accumulate: a later profile change must not drop the flip, or
	// the next boot silently undoes the change before it.
	if _, err := store.Apply(config.Patch{Profile: strPtr("720p60")}); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("after a profile change: %d writes, want 2", len(saved))
	}
	last := saved[1]
	if last.MirrorFlip == nil || !*last.MirrorFlip {
		t.Errorf("stored patch lost the earlier flip: %+v", last)
	}
	if last.Profile == nil || *last.Profile != "720p60" {
		t.Errorf("stored patch = %+v, want profile 720p60", last)
	}

	// A patch that changes nothing writes nothing: this file lives on the
	// FAT32 boot partition of a unit that gets unplugged (NFR-9), so a page
	// re-sending the value it already read must not touch the card.
	if _, err := store.Apply(config.Patch{Profile: strPtr("720p60")}); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Errorf("a no-op patch wrote to the card: %d writes, want 2", len(saved))
	}

	// And a rejected patch is not stored at all — the unit is not running
	// under it.
	if _, err := store.Apply(config.Patch{Profile: strPtr("4k120")}); err == nil {
		t.Fatal("invalid profile: want an error")
	}
	if len(saved) != 2 {
		t.Errorf("a rejected patch was persisted: %+v", saved)
	}
}

func strPtr(s string) *string { return &s }
