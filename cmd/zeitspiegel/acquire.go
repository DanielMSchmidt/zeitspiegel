package main

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/engine"
)

// displayRetryEvery is how often a unit without a screen tries to open one.
// Fast enough that plugging a cable in feels immediate to whoever is standing
// at the unit, slow enough that a failing SDL init is not attempted sixty
// times a second on the same goroutine the render loop runs on.
const displayRetryEvery = 2 * time.Second

// displayAcquirer opens the display, and keeps trying when it cannot.
//
// A field unit whose HDMI cable is not in — the TV is off, the cable is in
// the other mirror, the display woke up after the Pi did — has no connected
// DRM connector, so SDL/KMSDRM refuses to initialise. That used to be fatal:
// the process exited before the HTTP listener started, systemd restarted it a
// second later, and the unit spent the whole session invisible. It served no
// control page, joined no network and could not be asked what was wrong; the
// only record was the boot capture on its card, which is a poor way to learn
// that a cable is loose.
//
// So the display is no longer a precondition for running. Everything that
// does not need pixels — the API, capture, the role election — starts first,
// and the screen is picked up whenever it appears (FR-17).
//
// Not safe for concurrent use: try runs on the main goroutine, because SDL
// must be initialised on the thread the render loop will use (ARCHITECTURE
// §4). Only status reads across goroutines, which is what the mutex covers.
type displayAcquirer struct {
	open   func() (engine.Display, func() error, error)
	every  time.Duration
	logger *slog.Logger
	// now measures how long the open itself took, for the startup milestone.
	// Injected so the log line is deterministic in tests; nil means time.Now.
	now func() time.Time

	mu       sync.Mutex
	attempts uint64
	lastErr  string
	acquired bool
	headless bool // build has no display support; nothing to wait for

	lastTry time.Time
}

// try opens the display if it is time to try again, and reports what it got.
// A nil display means "not yet" — call again on a later tick. Once a display
// has been acquired, or the build turns out to have no display support at
// all, try stops calling open.
func (a *displayAcquirer) try(t time.Time) (engine.Display, func() error) {
	a.mu.Lock()
	done := a.acquired || a.headless
	last := a.lastTry
	a.mu.Unlock()
	if done {
		return nil, nil
	}
	// Zero lastTry = never tried: a unit with its screen already plugged in
	// must not wait out an interval before it has one.
	if !last.IsZero() && t.Sub(last) < a.every {
		return nil, nil
	}
	a.lastTry = t

	clock := a.now
	if clock == nil {
		clock = time.Now
	}
	t0 := clock()
	display, closeFn, err := a.open()
	took := clock().Sub(t0)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts++
	switch {
	case err != nil:
		// The retry runs for as long as the unit is powered, so the same
		// failure is logged once and a changed one is logged again — enough
		// to explain a dark unit without filling the card with one line per
		// attempt (NFR-8).
		if err.Error() != a.lastErr {
			a.logger.Error("display open failed, retrying", "err", err, "every", a.every)
		}
		a.lastErr = err.Error()
		return nil, nil
	case display == nil:
		// No sdl build tag: there is no screen on this binary by
		// construction, and no amount of waiting will produce one.
		a.headless = true
		return nil, nil
	default:
		// Always logged, on the first attempt as much as the hundredth:
		// deploy/zeitspiegel-boot-profile.sh greps this line out of the
		// journal to build a card's boot timing line-up (NFR-8, FR-12), and a
		// milestone that appears only on the unusual path is worse than none.
		a.logger.Info("display opened", "took", took.Round(time.Millisecond), "attempts", a.attempts)
		a.lastErr = ""
		a.acquired = true
		return display, closeFn
	}
}

// attachDisplay wires a freshly opened screen into the render loop and the
// rest of the process, and paints the splash so HDMI shows something while
// the camera is still enumerating.
//
// Everything here used to happen once at startup, before anything else ran.
// It happens on whichever tick the display turns up on now, which is why the
// mirror flip is taken from the runtime store rather than from the boot
// config: the control page has been answering since boot, so the value in
// force may already have moved.
func attachDisplay(rl *renderLoop, display engine.Display, store *runtimeStore,
	logger *slog.Logger, diagOut *atomic.Pointer[func() map[string]any]) {
	rl.display = display
	rl.pump = displayEvents(display)
	rl.setDelay = displayDelayFunc(display)
	rl.setWarmup = displayWarmupFunc(display)
	rl.splash = displaySplashFunc(display)
	rl.repaint = displayRepaintFunc(display)

	store.attachMirror(displayMirrorFunc(display))

	if diag := displayDiagFunc(display); diag != nil {
		d := diag()
		// software=true or refresh_hz≠60 each explain judder on their own.
		logger.Info("renderer", "name", d["renderer"], "software", d["software"], "refresh_hz", d["refresh_hz"])
		if diagOut != nil {
			diagOut.Store(&diag)
		}
	}
	// Paint the splash immediately so HDMI shows something while the camera
	// is enumerating — the render loop keeps repainting it every tick until
	// the first real frame arrives.
	if rl.splash != nil {
		if err := rl.splash(); err != nil {
			logger.Error("initial splash", "err", err)
		}
	}
}

// status is the expvar view: whether this unit has a screen, how hard it has
// tried, and what stopped it. On a card pulled from a venue this is the
// difference between "the mirror was dark" and "the mirror never had a
// display to draw on".
func (a *displayAcquirer) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"open":       a.acquired,
		"headless":   a.headless,
		"attempts":   a.attempts,
		"last_error": a.lastErr,
	}
}
