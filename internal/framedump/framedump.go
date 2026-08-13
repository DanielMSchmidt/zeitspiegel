// Package framedump writes the frames a unit is actually showing to disk, so
// they can be looked at later.
//
// A mirror that comes back "sharp but grey" cannot be diagnosed from a
// description of the colour, and a field unit has no network anybody can curl
// and no keyboard anybody can type on. Writing a bounded number of JPEGs where
// `make sd-logs` already collects them turns "it looked wrong" into bytes
// somebody can measure.
//
// It is a debugging aid, never a dependency: every failure here is logged and
// stepped over. On a sealed card the writes land in tmpfs and disappear, which
// is why this is enabled on development images.
package framedump

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/frame"
)

// Source is the newest frame in the buffer — ringbuf.Buffer satisfies it, as
// does the preview's provider.
type Source interface {
	Newest() (frame.Frame, error)
}

// Options configures a Dumper. Dir and Source are required; the rest have
// sensible defaults.
type Options struct {
	Dir      string
	Interval time.Duration
	Keep     int
	Source   Source
	Logger   *slog.Logger
	// Ticker paces the sampling, injected so this package stays wall-clock
	// free (hard rule 6) — cmd passes a real time.Ticker, tests fire it by
	// hand. nil means a real ticker, which only cmd should rely on.
	Ticker func(time.Duration) (<-chan time.Time, func())
}

// Dumper samples the buffer on an interval and keeps the newest few frames on
// disk.
type Dumper struct {
	opts  Options
	ticks atomic.Uint64
	last  uint64 // sequence number of the frame already on disk
}

const (
	defaultInterval = 5 * time.Second
	defaultKeep     = 30
)

// New returns a Dumper. It writes nothing until Run is called.
func New(o Options) *Dumper {
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.Keep < 1 {
		o.Keep = defaultKeep
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Ticker == nil {
		o.Ticker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &Dumper{opts: o}
}

// Ticks counts the sampling intervals that have elapsed, whether or not they
// wrote anything. Tests wait on it; nothing else should care.
func (d *Dumper) Ticks() uint64 { return d.ticks.Load() }

// Run samples until ctx is cancelled. Intended to be run in its own goroutine:
// writing a megabyte of JPEG to an SD card must never happen on the render
// thread (NFR-2).
func (d *Dumper) Run(ctx context.Context) {
	ticks, stop := d.opts.Ticker(d.opts.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			d.ticks.Add(1)
			d.sample()
		}
	}
}

// sample writes the newest frame, unless it is the one already written. A
// stalled capture would otherwise fill the card with copies of the moment
// things stopped, which says nothing that one copy does not.
func (d *Dumper) sample() {
	f, err := d.opts.Source.Newest()
	if err != nil {
		return // an empty buffer at boot is not news
	}
	if f.Seq == d.last {
		return
	}
	if err := os.MkdirAll(d.opts.Dir, 0o755); err != nil {
		d.opts.Logger.Warn("frame dump directory", "err", err, "dir", d.opts.Dir)
		return
	}
	name := filepath.Join(d.opts.Dir, fmt.Sprintf("frame-%s.jpg", SeqName(f.Seq)))
	// Written whole and renamed into place: a card pulled mid-write then
	// costs the new frame rather than leaving a half JPEG that looks like a
	// decoder bug.
	tmp := name + ".part"
	if err := os.WriteFile(tmp, f.JPEG, 0o644); err != nil {
		d.opts.Logger.Warn("frame dump write", "err", err, "file", name)
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, name); err != nil {
		d.opts.Logger.Warn("frame dump rename", "err", err, "file", name)
		_ = os.Remove(tmp)
		return
	}
	d.last = f.Seq
	d.prune()
}

// prune keeps the newest Keep files. Names sort in sequence order, so the
// oldest are simply the first.
func (d *Dumper) prune() {
	entries, err := os.ReadDir(d.opts.Dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jpg" {
			names = append(names, e.Name())
		}
	}
	if len(names) <= d.opts.Keep {
		return
	}
	sort.Strings(names)
	for _, name := range names[:len(names)-d.opts.Keep] {
		if err := os.Remove(filepath.Join(d.opts.Dir, name)); err != nil {
			d.opts.Logger.Warn("frame dump prune", "err", err, "file", name)
		}
	}
}

// SeqName renders a sequence number so that filenames sort in capture order
// for the life of a unit — zero-padded wide enough that 30 fps for a year does
// not roll over into a name that sorts before its predecessor.
func SeqName(seq uint64) string { return fmt.Sprintf("%012d", seq) }
