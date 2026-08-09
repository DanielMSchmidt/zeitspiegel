// Package fleet drives the role election against a real radio. It is the
// bridge between the pure decision logic in internal/netrole and whatever
// actually beacons and associates — nmcli on the appliance, a directory of
// files in the E2E lane.
//
// Everything it touches is an interface, so the whole election (cold start,
// failover, an ex-primary returning, a split brain collapsing) is testable
// on a laptop with no radios and no root (IT-11).
package fleet

import (
	"context"
	"errors"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/netrole"
)

// ErrScanUnavailable reports that a scan could not be performed because this
// unit is beaconing. It is not a failure to be retried away: a radio in AP
// mode genuinely cannot scan, which is the hardware fact the whole design is
// shaped around (ARCHITECTURE D8).
var ErrScanUnavailable = errors.New("scan unavailable: radio is hosting the access point")

// Radio is the hardware surface the election needs.
type Radio interface {
	// Scan returns the SSIDs currently on the air, or ErrScanUnavailable if
	// this unit is hosting an access point.
	Scan(ctx context.Context) ([]string, error)
	// ActivateAP starts hosting the shared network.
	ActivateAP(ctx context.Context) error
	// ActivateSTA associates with the shared network as a station.
	ActivateSTA(ctx context.Context) error
	// Down tears down whatever is currently active.
	Down(ctx context.Context) error
	// Associated reports whether the station link is up.
	Associated(ctx context.Context) (bool, error)
	// Gateway is the base URL of the unit hosting the network — the default
	// route, which is the primary by construction.
	Gateway(ctx context.Context) (string, error)
}

// PeerCounter is the live membership count the election watches to decide
// whether it might be half of a split brain (peers.Registry satisfies it).
type PeerCounter interface {
	Count() int
}

// DefaultInterval is how often the supervisor re-evaluates. It is well below
// every election timing, so the timings — not the tick rate — decide when
// anything happens.
const DefaultInterval = 2 * time.Second

// Supervisor drives one unit's election.
//
// It reports problems through OnError rather than logging, keeping the
// package free of a logger of its own; cmd decides what to do with them.
type Supervisor struct {
	// SSID is the one network name the whole fleet shares. There is only
	// ever one, whoever is hosting it, so the printed poster and
	// zeitspiegel.local stay valid across a failover.
	SSID    string
	Machine *netrole.Machine
	Radio   Radio
	Peers   PeerCounter
	Clock   netrole.Clock

	// Announce sends one heartbeat to the current primary. It is called
	// while this unit is a member, at most every AnnounceEvery. Nil disables
	// announcing.
	Announce      func(context.Context) error
	AnnounceEvery time.Duration

	// Ticker is injected so the loop stays wall-clock-free (hard rule 6).
	Ticker   func(time.Duration) (<-chan time.Time, func())
	Interval time.Duration

	// OnRole fires whenever the role changes — cmd uses it to set the
	// transient hostname and to surface the role in /status.
	OnRole  func(netrole.Role)
	OnError func(error)

	lastRole     netrole.Role
	lastAnnounce time.Time
	started      bool
}

// Run drives the election until ctx is done. No radio error ends the loop:
// an appliance must keep trying to find its place on the network for as long
// as it is powered.
func (s *Supervisor) Run(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	newTicker := s.Ticker
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	tick, stop := newTicker(interval)
	defer stop()

	s.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			s.Tick(ctx)
		}
	}
}

// Tick advances the election by one round: observe the radio, ask the
// machine what to do, do it. Exported so tests can drive it deterministically
// instead of racing a real ticker.
func (s *Supervisor) Tick(ctx context.Context) {
	obs := s.observe(ctx)
	s.perform(ctx, s.Machine.Step(obs))

	if role := s.Machine.Role(); role != s.lastRole || !s.started {
		s.lastRole, s.started = role, true
		if s.OnRole != nil {
			s.OnRole(role)
		}
	}
	s.maybeAnnounce(ctx)
}

// observe gathers what the machine needs, which depends on where the unit
// currently stands: a primary watches its membership count, a member watches
// its link, and anything else is looking for the shared network.
func (s *Supervisor) observe(ctx context.Context) netrole.Observation {
	switch s.Machine.Role() {
	case netrole.RolePrimary:
		var n int
		if s.Peers != nil {
			n = s.Peers.Count()
		}
		return netrole.Observation{Peers: n}

	case netrole.RoleMember:
		up, err := s.Radio.Associated(ctx)
		if err != nil {
			s.fail(err)
			return netrole.Observation{}
		}
		return netrole.Observation{Associated: up}

	default:
		ssids, err := s.Radio.Scan(ctx)
		if err != nil {
			// Either we are beaconing, or the scan genuinely failed. Both
			// mean "we do not know", and neither may be read as "nobody is
			// out there" — that is how a split brain gets made.
			if !errors.Is(err, ErrScanUnavailable) {
				s.fail(err)
			}
			return netrole.Observation{ScanFailed: true}
		}
		for _, ssid := range ssids {
			if ssid == s.SSID {
				return netrole.Observation{SSIDSeen: true}
			}
		}
		return netrole.Observation{}
	}
}

func (s *Supervisor) perform(ctx context.Context, act netrole.Action) {
	var err error
	switch act {
	case netrole.ActionJoin:
		err = s.Radio.ActivateSTA(ctx)
	case netrole.ActionBecomeAP:
		err = s.Radio.ActivateAP(ctx)
	case netrole.ActionDemote:
		err = s.Radio.Down(ctx)
	case netrole.ActionScan, netrole.ActionNone:
		// observe() already did the scanning; nothing to actuate.
	}
	if err != nil {
		s.fail(err)
	}
}

// maybeAnnounce heartbeats to the primary while this unit is a member. The
// throttle is on the clock, not the tick count, so the supervisor's tick rate
// stays free to change.
func (s *Supervisor) maybeAnnounce(ctx context.Context) {
	if s.Announce == nil || s.Machine.Role() != netrole.RoleMember {
		return
	}
	every := s.AnnounceEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	now := s.Clock.Now()
	if !s.lastAnnounce.IsZero() && now.Sub(s.lastAnnounce) < every {
		return
	}
	s.lastAnnounce = now
	if err := s.Announce(ctx); err != nil {
		s.fail(err)
	}
}

func (s *Supervisor) fail(err error) {
	if s.OnError != nil {
		s.OnError(err)
	}
}
