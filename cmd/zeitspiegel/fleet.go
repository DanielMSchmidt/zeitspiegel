package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/fleet"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/identity"
	"github.com/danielmschmidt/zeitspiegel/internal/netrole"
	"github.com/danielmschmidt/zeitspiegel/internal/peers"
)

// simSSID is the one network name the whole fleet shares. There is only ever
// one, whoever is hosting it, so the printed poster and zeitspiegel.local
// keep working across a failover (E-8).
const simSSID = "zeitspiegel"

// fleetOptions are the command-line overrides. All of them exist for
// development and for the E2E lane, where several units share one machine and
// therefore one /proc/cpuinfo; an appliance uses none of them.
type fleetOptions struct {
	UnitID    string
	UnitName  string
	NetSim    string
	NetScale  float64
	FleetSize int
}

// fleetRuntime is this unit's place in the fleet: who it is, who else is
// here, and whether it is currently hosting the network.
type fleetRuntime struct {
	unit      identity.Unit
	fleetSize int
	registry  *peers.Registry
	sup       *fleet.Supervisor
	role      atomic.Value // string
	logger    *slog.Logger
	manage    bool
}

// newFleetRuntime resolves this unit's identity and, when there is a fleet to
// join, builds the election supervisor. A fleet of one gets identity only:
// no election, no radio management, exactly the single-appliance behaviour
// E-7 describes.
func newFleetRuntime(cfg config.Config, opts fleetOptions, logger *slog.Logger) (*fleetRuntime, error) {
	unit, stable := identity.Resolve(identity.Sources{
		NameFile:   cfg.NameFile,
		OverrideID: opts.UnitID,
		Override:   opts.UnitName,
	})
	if !stable {
		// A unit id that changes every boot makes the peer registry and the
		// election tiebreak unreliable, so say so loudly rather than
		// quietly misbehaving later.
		logger.Warn("unit id is not stable across reboots: no CPU serial and no MAC could be read", "unit_id", unit.ID)
	}

	size := cfg.FleetSize
	if opts.FleetSize > 0 {
		size = opts.FleetSize
	}
	f := &fleetRuntime{unit: unit, fleetSize: size, logger: logger, manage: cfg.NetworkManage}
	f.role.Store(netrole.RolePrimary.String())
	logger.Info("unit identity", "unit_id", unit.ID, "name", unit.Name, "fleet_size", size)

	if size <= 1 {
		return f, nil
	}

	scale := opts.NetScale
	if scale < 1 {
		scale = 1
	}
	announceEvery := scaleDuration(peers.DefaultInterval, scale, 100*time.Millisecond)
	// Three missed heartbeats before a unit is considered gone: one lost
	// packet must never drop a card off the page.
	f.registry = peers.NewRegistry(unit.ID, 3*announceEvery, sysClock{})

	radio, err := f.newRadio(cfg, opts)
	if err != nil {
		return nil, err
	}
	if radio == nil {
		// A fleet was configured but nothing can drive the radio (a dev box
		// without --net-sim). Still serve the peer API so a unit pointed at
		// this one can register.
		logger.Info("fleet configured but the radio is not managed; no role election will run")
		return f, nil
	}

	machine := netrole.New(unit.ID, size, scaledTimings(scale), sysClock{})
	port, err := bindPort(cfg.Bind)
	if err != nil {
		return nil, err
	}
	announcer := &peers.Announcer{
		Self:    peers.Announcement{ID: unit.ID, Name: unit.Name, Port: port},
		Gateway: func() (string, error) { return radio.Gateway(context.Background()) },
		Client:  &http.Client{Timeout: 5 * time.Second},
		// The roster is what gives promotion its order if this unit's
		// primary disappears.
		OnRoster: machine.SetRoster,
	}

	f.sup = &fleet.Supervisor{
		SSID:          simSSID,
		Machine:       machine,
		Radio:         radio,
		Peers:         f.registry,
		Clock:         sysClock{},
		Announce:      func(ctx context.Context) error { _, err := announcer.Once(ctx); return err },
		AnnounceEvery: announceEvery,
		Interval:      scaleDuration(fleet.DefaultInterval, scale, 50*time.Millisecond),
		OnRole:        f.onRole,
		OnError: func(err error) {
			// Radio and announce failures are routine while a fleet is
			// settling — a member cannot reach a primary that is still
			// booting. They are worth seeing, never worth stopping for.
			logger.Debug("fleet", "err", err)
		},
	}
	return f, nil
}

func (f *fleetRuntime) newRadio(cfg config.Config, opts fleetOptions) (fleet.Radio, error) {
	if opts.NetSim != "" {
		port, err := bindPort(cfg.Bind)
		if err != nil {
			return nil, err
		}
		// The sim hands this URL to peers as the way to reach us, so it has
		// to be routable from the other processes on this machine.
		return newSimRadio(opts.NetSim, f.unit.ID, fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	if cfg.NetworkManage {
		return newNMCLIRadio(), nil
	}
	return nil, nil
}

// onRole reacts to a role change: the unit hosting the network answers to
// zeitspiegel.local so the printed poster keeps working, and everybody else
// takes a name of their own so mDNS does not collide.
func (f *fleetRuntime) onRole(role netrole.Role) {
	f.role.Store(role.String())
	f.logger.Info("fleet role", "role", role.String(), "unit_id", f.unit.ID)

	if !f.manage {
		return
	}
	host := "zeitspiegel"
	if role != netrole.RolePrimary {
		host = "zeitspiegel-" + f.unit.ID
	}
	// --transient is required: the persistent form writes /etc/hostname,
	// which fails on the sealed read-only root. Avahi follows the transient
	// hostname, which is all mDNS needs.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "hostnamectl", "--transient", "set-hostname", host).CombinedOutput(); err != nil {
		f.logger.Warn("set transient hostname", "host", host, "err", err, "output", string(out))
	}
}

// Role is what this unit reports in /status. A unit with no election running
// is hosting its own network, so it is primary by definition.
func (f *fleetRuntime) Role() string {
	if s, ok := f.role.Load().(string); ok {
		return s
	}
	return netrole.RolePrimary.String()
}

// PeerStore is the fleet API's backing store, or nil for a lone appliance —
// in which case the /api/v1/peers routes are not registered at all.
func (f *fleetRuntime) PeerStore() httpapi.PeerStore {
	if f.registry == nil {
		return nil
	}
	return f.registry
}

// Run drives the election until ctx is done. It returns immediately for a
// unit that has no election to run.
func (f *fleetRuntime) Run(ctx context.Context) error {
	if f.sup == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.sup.Run(ctx)
}

// scaledTimings compresses the election so the E2E lane can watch a whole
// failover in seconds. Production runs at scale 1.
func scaledTimings(scale float64) netrole.Timings {
	t := netrole.DefaultTimings()
	if scale <= 1 {
		return t
	}
	div := func(d time.Duration) time.Duration {
		return time.Duration(float64(d) / scale)
	}
	t.Stagger, t.PromoteStep = div(t.Stagger), div(t.PromoteStep)
	t.HealAfter, t.JoinTimeout = div(t.HealAfter), div(t.JoinTimeout)
	return t
}

func scaleDuration(d time.Duration, scale float64, floor time.Duration) time.Duration {
	out := time.Duration(float64(d) / scale)
	if out < floor {
		return floor
	}
	return out
}

// bindPort is the port peers should use to reach this unit. It is announced
// as-is; the primary pairs it with the address the connection came from.
func bindPort(bind string) (int, error) {
	_, portStr, err := net.SplitHostPort(bind)
	if err != nil {
		return 0, fmt.Errorf("bind %q: %w", bind, err)
	}
	if portStr == "" {
		return 80, nil
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("bind %q: port: %w", bind, err)
	}
	return port, nil
}
