// Package netrole decides, at runtime, which role a unit takes on the shared
// Wi-Fi network: host the access point (primary) or join it (member). Because
// the decision is made from what the radio can see rather than from baked
// configuration, every appliance ships the identical SD image, and units can
// be added to — or removed from — an installation at any time (E-8).
//
// The package is pure — no I/O, no wall clock (hard rule 6). The caller drives
// it: each Step returns an Action to perform against a real radio, and what
// the radio reported goes back in on the next Step as an Observation.
//
// The shape of the machine is forced by one hardware fact: a Wi-Fi radio in AP
// mode cannot scan. A host therefore can never see that a second host is
// beaconing the same SSID, so a split brain is undetectable by observation.
// What a host CAN see, cheaply and even in AP mode, is whether anyone at all
// is using its network — associated stations include member units and guests'
// phones alike. That yields the central rule: a host serving anybody holds its
// network; only a host serving nobody probes for other networks, and since
// nobody is attached, probing kicks no one (ARCHITECTURE D8).
package netrole

import (
	"hash/fnv"
	"sort"
	"time"
)

// Clock abstracts time so the machine stays wall-clock-free; tests use
// synth.FakeClock.
type Clock interface {
	Now() time.Time
}

// Role is what this unit currently is on the network.
type Role int

const (
	// RoleUnknown is the boot state, and the state while a unit is between
	// roles (searching, waiting its turn to promote, or probing).
	RoleUnknown Role = iota
	// RolePrimary means this unit hosts the access point.
	RolePrimary
	// RoleMember means this unit is associated to somebody else's AP.
	RoleMember
)

func (r Role) String() string {
	switch r {
	case RolePrimary:
		return "primary"
	case RoleMember:
		return "member"
	default:
		return "unknown"
	}
}

// Action is what the caller should do to the radio before the next Step.
type Action int

const (
	// ActionNone means stay as you are.
	ActionNone Action = iota
	// ActionScan means scan for the shared SSID and report it back.
	ActionScan
	// ActionJoin means associate with the shared SSID as a station.
	ActionJoin
	// ActionBecomeAP means bring up the access point.
	ActionBecomeAP
	// ActionDemote means tear the access point down; the machine returns to
	// searching and will either join another network or come back up.
	ActionDemote
)

func (a Action) String() string {
	switch a {
	case ActionScan:
		return "scan"
	case ActionJoin:
		return "join"
	case ActionBecomeAP:
		return "become-ap"
	case ActionDemote:
		return "demote"
	default:
		return "none"
	}
}

// Timings are injected so the E2E lane can compress a failover into seconds
// (hard rule 6 — nothing here reads the wall clock).
type Timings struct {
	// Stagger is the spacing between AP-claim slots; a unit waits
	// slot×Stagger before claiming an unclaimed SSID. The slot count is
	// fixed (StaggerSlots) — there is no fleet size to derive it from — and
	// the total is kept short: the stagger only reduces the chance of a
	// simultaneous claim, because a claim collision is healed by the idle
	// probe anyway, and a solo dancer's Wi-Fi must not take a minute to
	// appear.
	Stagger      time.Duration
	StaggerSlots int
	// PromoteStep is the spacing between promotion positions after the host
	// disappears: the unit at roster position n waits n×PromoteStep.
	PromoteStep time.Duration
	// HealAfter is how long a host serving nobody at all waits before
	// probing for other networks. Each fruitless probe doubles the next
	// wait, up to MaxHealMult.
	HealAfter time.Duration
	// JoinTimeout bounds how long an association may take before the unit
	// gives up and goes back to searching.
	JoinTimeout time.Duration
	// MaxHealMult caps the probe backoff, so a genuinely lonely unit
	// converges to a quiet cadence instead of flapping.
	MaxHealMult int
}

// DefaultTimings are the production values. HealAfter is deliberately well
// above PromoteStep: a promotion race must have fully settled before an empty
// audience is allowed to look like a split brain. PromoteStep is short —
// after a host's power is cut, position 1 brings the network back in roughly
// detection (~2-4 s) + 10 s + AP bring-up (~3-5 s), and phones rejoin the
// same open SSID on their own.
func DefaultTimings() Timings {
	return Timings{
		Stagger:      3 * time.Second,
		StaggerSlots: 4,
		PromoteStep:  10 * time.Second,
		HealAfter:    90 * time.Second,
		JoinTimeout:  30 * time.Second,
		MaxHealMult:  8,
	}
}

// Observation is what the radio reported since the last Step.
type Observation struct {
	// SSIDSeen reports that the shared SSID was found by the last scan.
	SSIDSeen bool
	// ScanFailed reports that the scan could not be performed — normally
	// because this unit is itself beaconing. It must never be read as
	// "nobody is out there": that is precisely how a split brain is made.
	ScanFailed bool
	// Associated reports that the station link is up (members only).
	Associated bool
	// Peers is the number of other units currently registered with this
	// unit (hosts only).
	Peers int
	// Stations is the number of clients associated to this unit's AP —
	// member units and guests' phones alike (hosts only). Anyone at all
	// being served is reason to hold the network.
	Stations int
}

type state int

const (
	stateSearching state = iota
	stateJoining
	stateMember
	statePendingPromote
	statePrimary
)

// Machine is the election state machine for one unit. It is not safe for
// concurrent use; the caller drives it from a single goroutine.
type Machine struct {
	id  string
	t   Timings
	clk Clock

	role   Role
	state  state
	roster []string

	// deadline's meaning depends on the state: end of the stagger while
	// searching, association timeout while joining, promotion time while
	// waiting to promote.
	deadline time.Time
	// idleSince is when a host last saw an empty audience begin — zero
	// while anyone (unit or phone) is being served.
	idleSince time.Time
	// healAttempt counts consecutive fruitless probes, driving the backoff.
	healAttempt int
}

// New returns a Machine for a unit with the given stable id. There is no
// fleet size: how many units share the network is discovered, not configured,
// so one image serves an installation that grows over time (E-8).
func New(id string, t Timings, clk Clock) *Machine {
	if t.StaggerSlots < 1 {
		t.StaggerSlots = 1
	}
	return &Machine{id: id, t: t, clk: clk}
}

// Role reports the current role.
func (m *Machine) Role() Role { return m.role }

// SetRoster records the unit ids last known to be on the network, which is
// what gives promotion its deterministic order. The caller passes the roster
// it got from the host when registering.
func (m *Machine) SetRoster(ids []string) {
	m.roster = append([]string(nil), ids...)
	sort.Strings(m.roster)
}

// StaggerDelay is this unit's wait before claiming an unclaimed SSID. It is
// derived from the unit id, so it is stable across reboots and differs
// between units without any coordination.
func (m *Machine) StaggerDelay() time.Duration {
	return time.Duration(m.slot()) * m.t.Stagger
}

func (m *Machine) slot() int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(m.id))
	return int(h.Sum32() % uint32(m.t.StaggerSlots))
}

// promoteDelay is how long this unit waits before trying to take over after
// the host disappears: its position in the roster times PromoteStep. The
// lowest surviving id goes first, and if that unit is dead too the next one
// follows automatically, so the fleet cannot deadlock waiting for a
// designated successor.
func (m *Machine) promoteDelay() time.Duration {
	for i, id := range m.roster {
		if id == m.id {
			return time.Duration(i) * m.t.PromoteStep
		}
	}
	// No roster yet (we never registered): fall back to the stagger slot so
	// units still differ.
	return time.Duration(m.slot()) * m.t.PromoteStep
}

// healDelay is how long an audience-less host waits before probing. It backs
// off on every fruitless probe and carries the unit's stagger offset so two
// hosts in a split brain do not probe in lockstep.
func (m *Machine) healDelay() time.Duration {
	mult := 1
	for i := 0; i < m.healAttempt; i++ {
		mult *= 2
		if mult >= m.t.MaxHealMult {
			mult = m.t.MaxHealMult
			break
		}
	}
	return time.Duration(mult)*m.t.HealAfter + m.StaggerDelay()
}

// Step advances the machine by one observation and returns the action to
// perform. It is safe to call as often as the caller likes; the timings, not
// the call rate, decide when anything happens.
func (m *Machine) Step(o Observation) Action {
	now := m.clk.Now()

	switch m.state {
	case stateSearching:
		// Somebody is already hosting: join, never claim a second AP.
		if o.SSIDSeen {
			return m.join(now)
		}
		// A refused scan tells us nothing. Keep looking rather than
		// claiming the SSID on the strength of it.
		if o.ScanFailed {
			return ActionScan
		}
		if m.deadline.IsZero() {
			m.deadline = now.Add(m.StaggerDelay())
			if now.Before(m.deadline) {
				return ActionScan
			}
			// Slot 0: nothing to wait for.
		}
		if now.Before(m.deadline) {
			return ActionScan
		}
		m.deadline = time.Time{}
		return m.becomeAP()

	case stateJoining:
		if o.Associated {
			m.state, m.deadline = stateMember, time.Time{}
			return ActionNone
		}
		if now.Before(m.deadline) {
			return ActionNone
		}
		// The association never came up. Back to searching.
		m.role, m.state, m.deadline = RoleUnknown, stateSearching, time.Time{}
		return ActionScan

	case stateMember:
		if o.Associated {
			return ActionNone
		}
		// The host went away. Wait our turn rather than racing every other
		// survivor onto the air at once.
		m.role, m.state = RoleUnknown, statePendingPromote
		m.deadline = now.Add(m.promoteDelay())
		return ActionNone

	case statePendingPromote:
		// The network is back — either the loss was a blip or a faster
		// survivor already promoted. Rejoining is always safe (it cannot
		// create a second AP), and it turns a transient Wi-Fi hiccup into
		// seconds of absence instead of a whole promotion slot.
		if o.SSIDSeen {
			return m.join(now)
		}
		if now.Before(m.deadline) {
			return ActionNone
		}
		m.state, m.deadline = stateSearching, time.Time{}
		return ActionScan

	case statePrimary:
		// Anyone at all being served — a registered unit or just a phone —
		// is reason to hold the network. This is what makes a solo mirror
		// with an audience rock-solid: it never takes its Wi-Fi away.
		if o.Peers > 0 || o.Stations > 0 {
			m.idleSince = time.Time{}
			m.healAttempt = 0
			return ActionNone
		}
		if m.idleSince.IsZero() {
			m.idleSince = now
			return ActionNone
		}
		if now.Sub(m.idleSince) < m.healDelay() {
			return ActionNone
		}
		// Nobody has used this network for a while. We might be one half of
		// a split brain — and being in AP mode, we cannot scan to find out.
		// Drop the AP and look; with zero stations there is nobody to kick,
		// so the probe costs nothing. Either we find the other network and
		// join it, or nothing is there and we come straight back up.
		m.healAttempt++
		m.role, m.state = RoleUnknown, stateSearching
		m.deadline, m.idleSince = time.Time{}, time.Time{}
		return ActionDemote
	}

	return ActionNone
}

func (m *Machine) join(now time.Time) Action {
	m.role, m.state = RoleMember, stateJoining
	m.deadline = now.Add(m.t.JoinTimeout)
	return ActionJoin
}

func (m *Machine) becomeAP() Action {
	m.role, m.state = RolePrimary, statePrimary
	m.idleSince = time.Time{}
	return ActionBecomeAP
}
