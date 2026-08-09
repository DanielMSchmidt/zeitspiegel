// Package netrole decides, at runtime, which role a unit takes on the shared
// Wi-Fi network: host the access point (primary) or join it (member). Because
// the decision is made from what the radio can see rather than from baked
// configuration, every appliance can ship the identical SD image (E-8).
//
// The package is pure — no I/O, no wall clock (hard rule 6). The caller drives
// it: each Step returns an Action to perform against a real radio, and what
// the radio reported goes back in on the next Step as an Observation.
//
// The shape of the machine is forced by one hardware fact: a Wi-Fi radio in AP
// mode cannot scan. A primary therefore can never see that a second primary is
// beaconing the same SSID, so a split brain is undetectable by observation.
// Two mechanisms cover it instead — an id-derived stagger that makes a
// simultaneous claim unlikely, and a blind self-heal that demotes a primary
// which has stayed short of its expected fleet (ARCHITECTURE D8).
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
	// roles (searching, waiting its turn to promote, or just demoted).
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
	// Stagger is the spacing between AP-claim slots. A unit waits
	// slot×Stagger before claiming an unclaimed SSID, which is what stops a
	// shared power strip from making every unit primary at once.
	Stagger time.Duration
	// PromoteStep is the spacing between promotion positions after the
	// primary disappears: the unit at roster position n waits n×PromoteStep.
	PromoteStep time.Duration
	// HealAfter is how long a primary tolerates being short of its fleet
	// before demoting to check whether it is half of a split brain. Each
	// fruitless heal doubles the next wait, up to MaxHealMult.
	HealAfter time.Duration
	// JoinTimeout bounds how long an association may take before the unit
	// gives up and goes back to searching.
	JoinTimeout time.Duration
	// MaxHealMult caps the exponential backoff on fruitless heals, so a
	// legitimately switched-off peer costs a few brief interruptions rather
	// than one every HealAfter forever.
	MaxHealMult int
}

// DefaultTimings are the production values. HealAfter is deliberately well
// above PromoteStep: a promotion race must have fully settled before a short
// fleet is allowed to look like a split brain.
func DefaultTimings() Timings {
	return Timings{
		Stagger:     8 * time.Second,
		PromoteStep: 15 * time.Second,
		HealAfter:   90 * time.Second,
		JoinTimeout: 30 * time.Second,
		MaxHealMult: 8,
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
	// unit (primaries only).
	Peers int
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
	id    string
	fleet int
	t     Timings
	clk   Clock

	role   Role
	state  state
	roster []string

	// deadline's meaning depends on the state: end of the stagger while
	// searching, association timeout while joining, promotion time while
	// waiting to promote.
	deadline time.Time
	// shortSince is when a primary first noticed it was short of its fleet;
	// zero means it is not currently short.
	shortSince time.Time
	// healAttempt counts consecutive fruitless heals, driving the backoff.
	healAttempt int
}

// New returns a Machine for a unit with the given stable id. fleetSize is the
// number of units expected on the network and is identical on every card; 1
// disables the stagger and the self-heal entirely, so a single appliance
// behaves exactly as it did before multi-unit support (E-7).
func New(id string, fleetSize int, t Timings, clk Clock) *Machine {
	if fleetSize < 1 {
		fleetSize = 1
	}
	return &Machine{id: id, fleet: fleetSize, t: t, clk: clk}
}

// Role reports the current role.
func (m *Machine) Role() Role { return m.role }

// SetRoster records the unit ids last known to be on the network, which is
// what gives promotion its deterministic order. The caller passes the roster
// it got from the primary when registering.
func (m *Machine) SetRoster(ids []string) {
	m.roster = append([]string(nil), ids...)
	sort.Strings(m.roster)
}

// StaggerDelay is this unit's wait before claiming an unclaimed SSID. It is
// derived from the unit id, so it is stable across reboots and differs
// between units without any coordination.
func (m *Machine) StaggerDelay() time.Duration {
	if m.fleet <= 1 {
		return 0
	}
	return time.Duration(m.slot()) * m.t.Stagger
}

func (m *Machine) slot() int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(m.id))
	return int(h.Sum32() % uint32(m.fleet))
}

// promoteDelay is how long this unit waits before trying to take over after
// the primary disappears: its position in the roster times PromoteStep. The
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

// healDelay is how long a short-fleet primary waits before demoting. It backs
// off on every fruitless heal and carries the unit's stagger offset so two
// primaries in a split brain do not demote in lockstep.
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
		if m.fleet <= 1 {
			return m.becomeAP()
		}
		if m.deadline.IsZero() {
			m.deadline = now.Add(m.StaggerDelay())
			return ActionScan
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
		// The primary went away. Wait our turn rather than racing every
		// other survivor onto the air at once.
		m.role, m.state = RoleUnknown, statePendingPromote
		m.deadline = now.Add(m.promoteDelay())
		return ActionNone

	case statePendingPromote:
		if now.Before(m.deadline) {
			return ActionNone
		}
		m.state, m.deadline = stateSearching, time.Time{}
		return ActionScan

	case statePrimary:
		// Nothing to be short of, or the fleet is all here.
		if m.fleet <= 1 || o.Peers >= m.fleet-1 {
			m.shortSince = time.Time{}
			m.healAttempt = 0
			return ActionNone
		}
		if m.shortSince.IsZero() {
			m.shortSince = now
			return ActionNone
		}
		if now.Sub(m.shortSince) < m.healDelay() {
			return ActionNone
		}
		// We have been short for long enough that we might be one half of a
		// split brain — and being in AP mode, we cannot scan to find out.
		// Drop the AP and look. Either we find the other network and join
		// it, or nothing is there and we come straight back up.
		m.healAttempt++
		m.role, m.state = RoleUnknown, stateSearching
		m.deadline, m.shortSince = time.Time{}, time.Time{}
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
	m.shortSince = time.Time{}
	return ActionBecomeAP
}
