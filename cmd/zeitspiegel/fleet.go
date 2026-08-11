package main

import (
	"context"
	"expvar"
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
	UnitID   string
	UnitName string
	NetSim   string
	NetScale float64
}

// fleetRuntime is this unit's place in the fleet: who it is, who else is
// here, and whether it is currently hosting the network. There is no fleet
// size anywhere: how many units share the network is discovered at runtime,
// so an installation can grow one card at a time (E-8).
type fleetRuntime struct {
	unit     identity.Unit
	registry *peers.Registry
	sup      *fleet.Supervisor
	role     atomic.Value // string
	logger   *slog.Logger
	manage   bool

	roleChanges      expvar.Int
	healProbes       expvar.Int
	announceFailures expvar.Int
}

// newFleetRuntime resolves this unit's identity and builds the election
// supervisor. The supervisor runs whether or not any other unit exists —
// both NetworkManager profiles ship with autoconnect=false so they cannot
// race the election, which means this binary is the only thing that ever
// brings a network up, a lone appliance included.
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

	f := &fleetRuntime{unit: unit, logger: logger, manage: cfg.NetworkManage}
	f.role.Store(netrole.RolePrimary.String())
	logger.Info("unit identity", "unit_id", unit.ID, "name", unit.Name)

	scale := opts.NetScale
	if scale < 1 {
		scale = 1
	}
	announceEvery := scaleDuration(peers.DefaultInterval, scale, 100*time.Millisecond)
	// The registry is always on: "no peers yet" is an ordinary answer in an
	// installation that grows one unit at a time. Three missed heartbeats
	// before a unit is considered gone — one lost packet must never drop a
	// card off the page.
	f.registry = peers.NewRegistry(unit.ID, 3*announceEvery, sysClock{})

	// A field appliance debugging aid, not just dev candy: role changes and
	// probe counts are how a headless unit's election history is read back
	// (NFR-8).
	expvar.Publish("zeitspiegel_fleet", expvar.Func(func() any {
		return map[string]any{
			"unit_id":           unit.ID,
			"role":              f.Role(),
			"role_changes":      f.roleChanges.Value(),
			"heal_probes":       f.healProbes.Value(),
			"announce_failures": f.announceFailures.Value(),
			"peers":             f.registry.Count(),
		}
	}))

	radio, err := f.newRadio(cfg, opts)
	if err != nil {
		return nil, err
	}
	if radio == nil {
		// Nothing can drive the radio here (a dev box, or `make run-synth`).
		// Still serve the peer API so a unit pointed at this one can
		// register.
		logger.Info("radio not managed; no role election will run")
		return f, nil
	}

	machine := netrole.New(unit.ID, scaledTimings(scale), sysClock{})
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
		StationEvery:  scaleDuration(10*time.Second, scale, 100*time.Millisecond),
		Interval:      scaleDuration(fleet.DefaultInterval, scale, 50*time.Millisecond),
		OnRole:        f.onRole,
		OnError: func(err error) {
			// Radio and announce failures are routine while a fleet is
			// settling — a member cannot reach a primary that is still
			// booting. They are worth seeing in the journal (a headless
			// appliance has nothing else), never worth stopping for.
			f.announceFailures.Add(1)
			logger.Info("fleet", "err", err)
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
	if prev, _ := f.role.Load().(string); prev == netrole.RolePrimary.String() && role == netrole.RoleUnknown {
		// A host stepping down to look around is the self-heal probe in
		// action; worth counting separately from ordinary role churn.
		f.healProbes.Add(1)
	}
	f.roleChanges.Add(1)
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
	// which fails on the sealed read-only root.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "hostnamectl", "--transient", "set-hostname", host).CombinedOutput(); err != nil {
		f.logger.Warn("set transient hostname", "host", host, "err", err, "output", string(out))
	}

	// Avahi does NOT follow kernel hostname changes: it picks its mDNS name
	// at daemon startup, so without this call zeitspiegel.local would stay
	// wherever the boot-time name race left it and go dark after a failover
	// — the printed poster's URL, dead until a power cycle.
	// avahi-set-host-name renames the running daemon live; that the rename
	// does not persist across daemon restarts is exactly right on a
	// read-only root, where the next boot re-elects anyway. Skip silently
	// when the tool is absent (dev machines — avahi-utils is baked into the
	// appliance image).
	if _, err := exec.LookPath("avahi-set-host-name"); err == nil {
		if out, err := exec.CommandContext(ctx, "avahi-set-host-name", host).CombinedOutput(); err != nil {
			f.logger.Warn("set mDNS hostname", "host", host, "err", err, "output", string(out))
		}
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
	// StaggerSlots is a count, not a duration; it survives scaling as-is.
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
