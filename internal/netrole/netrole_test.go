package netrole_test

import (
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/netrole"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

// testTimings are compressed so the table runs in fake-clock steps that are
// easy to read; the production values live in netrole.DefaultTimings.
func testTimings() netrole.Timings {
	return netrole.Timings{
		Stagger:     10 * time.Second,
		PromoteStep: 20 * time.Second,
		HealAfter:   90 * time.Second,
		JoinTimeout: 30 * time.Second,
		MaxHealMult: 8,
	}
}

func newMachine(t *testing.T, id string, fleet int) (*netrole.Machine, *synth.FakeClock) {
	t.Helper()
	clk := synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	return netrole.New(id, fleet, testTimings(), clk), clk
}

// UT-19: a unit configured as a fleet of one claims the AP immediately —
// today's single-appliance behaviour must not regress (E-7).
func TestLoneUnitBecomesPrimaryImmediately(t *testing.T) {
	m, _ := newMachine(t, "a1", 1)

	if got := m.Step(netrole.Observation{}); got != netrole.ActionBecomeAP {
		t.Fatalf("first step = %v, want %v", got, netrole.ActionBecomeAP)
	}
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RolePrimary)
	}
}

// UT-19: the shared SSID is already on the air ⇒ join it, never claim it.
func TestSSIDSeenJoins(t *testing.T) {
	m, _ := newMachine(t, "a1", 3)

	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("step = %v, want %v", got, netrole.ActionJoin)
	}
	if m.Role() != netrole.RoleMember {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RoleMember)
	}
}

// UT-19: with nothing on the air a unit waits out its id-derived stagger
// slot before claiming the AP, rescanning throughout — this is what stops a
// shared power strip from making every unit primary at once.
func TestStaggerBeforeClaimingAP(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)

	if got := m.Step(netrole.Observation{}); got != netrole.ActionScan {
		t.Fatalf("first step = %v, want %v", got, netrole.ActionScan)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("role = %v, want %v (must not claim during the stagger)", m.Role(), netrole.RoleUnknown)
	}

	// Somebody else comes up mid-stagger: join instead of claiming.
	clk.Advance(time.Second)
	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("mid-stagger with SSID = %v, want %v", got, netrole.ActionJoin)
	}
}

// UT-19: slots are derived from the unit id, so two units in the same fleet
// do not decide at the same instant.
func TestStaggerSlotsDifferByID(t *testing.T) {
	const fleet = 3
	seen := map[time.Duration]string{}
	distinct := 0
	for _, id := range []string{"a1", "b2", "c3"} {
		m, _ := newMachine(t, id, fleet)
		d := m.StaggerDelay()
		if d < 0 || d >= time.Duration(fleet)*testTimings().Stagger {
			t.Fatalf("id %q slot delay %v out of range", id, d)
		}
		if _, dup := seen[d]; !dup {
			distinct++
		}
		seen[d] = id
	}
	if distinct < 2 {
		t.Fatalf("all three ids landed in one slot (%v); stagger would not help", seen)
	}
}

// UT-19: once the stagger expires with still nothing on the air, claim it.
func TestClaimsAPAfterStagger(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})

	clk.Advance(m.StaggerDelay() + time.Second)
	if got := m.Step(netrole.Observation{}); got != netrole.ActionBecomeAP {
		t.Fatalf("after stagger = %v, want %v", got, netrole.ActionBecomeAP)
	}
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RolePrimary)
	}
}

// UT-19: a scan refused because this unit is itself beaconing must never be
// read as "nobody is out there" — that is exactly how a split brain would be
// created. It is handled, not fatal (see ARCHITECTURE D8).
func TestRefusedScanNeverClaimsAP(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})

	clk.Advance(m.StaggerDelay() + time.Minute)
	for i := 0; i < 5; i++ {
		if got := m.Step(netrole.Observation{ScanFailed: true}); got != netrole.ActionScan {
			t.Fatalf("step %d with refused scan = %v, want %v", i, got, netrole.ActionScan)
		}
		if m.Role() == netrole.RolePrimary {
			t.Fatal("claimed the AP on the strength of a failed scan")
		}
		clk.Advance(time.Second)
	}

	// A clean scan afterwards resolves it normally.
	if got := m.Step(netrole.Observation{}); got != netrole.ActionBecomeAP {
		t.Fatalf("after a clean scan = %v, want %v", got, netrole.ActionBecomeAP)
	}
}

// UT-19: an association that never comes up falls back to searching instead
// of sitting in a half-joined state forever.
func TestJoinTimeoutFallsBackToSearching(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{SSIDSeen: true})

	clk.Advance(testTimings().JoinTimeout + time.Second)
	if got := m.Step(netrole.Observation{Associated: false}); got != netrole.ActionScan {
		t.Fatalf("after join timeout = %v, want %v", got, netrole.ActionScan)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RoleUnknown)
	}
}

// UT-19: a settled member stays put and issues no actions.
func TestMemberStaysPut(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{SSIDSeen: true})
	m.Step(netrole.Observation{Associated: true})

	for i := 0; i < 10; i++ {
		clk.Advance(10 * time.Second)
		if got := m.Step(netrole.Observation{Associated: true}); got != netrole.ActionNone {
			t.Fatalf("settled member step %d = %v, want %v", i, got, netrole.ActionNone)
		}
		if m.Role() != netrole.RoleMember {
			t.Fatalf("role drifted to %v", m.Role())
		}
	}
}

// UT-19: when the primary disappears the surviving members promote in
// roster order, so the lowest surviving id takes over first and the next one
// covers automatically if that unit is dead too. This is the owner's "the
// second one promotes" rule, generalised so it cannot deadlock.
func TestPromotionOrderFollowsRosterPosition(t *testing.T) {
	roster := []string{"a1", "b2", "c3"} // a1 was primary

	for _, tc := range []struct {
		id   string
		want time.Duration
	}{
		{"b2", 1 * testTimings().PromoteStep},
		{"c3", 2 * testTimings().PromoteStep},
	} {
		m, clk := newMachine(t, tc.id, 3)
		m.SetRoster(roster)
		m.Step(netrole.Observation{SSIDSeen: true})
		m.Step(netrole.Observation{Associated: true})

		// The AP goes away.
		if got := m.Step(netrole.Observation{Associated: false}); got != netrole.ActionNone {
			t.Fatalf("%s: losing the AP = %v, want %v (must wait its turn)", tc.id, got, netrole.ActionNone)
		}

		clk.Advance(tc.want - time.Second)
		if got := m.Step(netrole.Observation{}); got != netrole.ActionNone {
			t.Fatalf("%s: promoted early at %v", tc.id, tc.want-time.Second)
		}
		clk.Advance(2 * time.Second)
		if got := m.Step(netrole.Observation{}); got != netrole.ActionScan {
			t.Fatalf("%s: at %v = %v, want %v", tc.id, tc.want, got, netrole.ActionScan)
		}
	}
}

// UT-19: a member that loses the AP and then finds it back (somebody else
// promoted first) rejoins rather than claiming a second AP.
func TestLosingAPThenSeeingItAgainRejoins(t *testing.T) {
	m, clk := newMachine(t, "c3", 3)
	m.SetRoster([]string{"a1", "b2", "c3"})
	m.Step(netrole.Observation{SSIDSeen: true})
	m.Step(netrole.Observation{Associated: true})
	m.Step(netrole.Observation{Associated: false})

	clk.Advance(3 * testTimings().PromoteStep)
	if got := m.Step(netrole.Observation{}); got != netrole.ActionScan {
		t.Fatalf("promote window = %v, want %v", got, netrole.ActionScan)
	}
	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("SSID back = %v, want %v", got, netrole.ActionJoin)
	}
}

// UT-19: a primary with the whole fleet registered never demotes, however
// long it runs.
func TestHealthyPrimaryNeverDemotes(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})

	for i := 0; i < 50; i++ {
		clk.Advance(time.Minute)
		if got := m.Step(netrole.Observation{Peers: 2}); got != netrole.ActionNone {
			t.Fatalf("healthy primary step %d = %v, want %v", i, got, netrole.ActionNone)
		}
	}
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RolePrimary)
	}
}

// UT-19: a fleet of one never self-heals — there is nothing to be short of.
func TestSingleUnitFleetNeverDemotes(t *testing.T) {
	m, clk := newMachine(t, "a1", 1)
	m.Step(netrole.Observation{})

	for i := 0; i < 50; i++ {
		clk.Advance(time.Minute)
		if got := m.Step(netrole.Observation{Peers: 0}); got != netrole.ActionNone {
			t.Fatalf("lone primary step %d = %v, want %v", i, got, netrole.ActionNone)
		}
	}
}

// UT-19: a primary that stays short of its fleet eventually demotes and
// rescans — the blind recovery from a split brain, which no scan can detect
// because a beaconing radio cannot scan (ARCHITECTURE D8).
func TestShortFleetPrimaryDemotes(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("setup: role = %v", m.Role())
	}

	// Short, but not yet for long enough.
	if got := m.Step(netrole.Observation{Peers: 0}); got != netrole.ActionNone {
		t.Fatalf("first short observation = %v, want %v", got, netrole.ActionNone)
	}
	clk.Advance(testTimings().HealAfter / 2)
	if got := m.Step(netrole.Observation{Peers: 0}); got != netrole.ActionNone {
		t.Fatalf("half-way = %v, want %v", got, netrole.ActionNone)
	}

	clk.Advance(testTimings().HealAfter + m.StaggerDelay() + time.Second)
	if got := m.Step(netrole.Observation{Peers: 0}); got != netrole.ActionDemote {
		t.Fatalf("after the heal window = %v, want %v", got, netrole.ActionDemote)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("role after demote = %v, want %v", m.Role(), netrole.RoleUnknown)
	}
}

// UT-19: the fleet coming back resets the short-fleet timer, so a member
// that reboots does not eventually cost everyone an outage.
func TestPeerReturningResetsHealTimer(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})

	m.Step(netrole.Observation{Peers: 0})
	clk.Advance(testTimings().HealAfter - time.Second)
	m.Step(netrole.Observation{Peers: 2}) // everyone is back
	clk.Advance(testTimings().HealAfter - time.Second)
	if got := m.Step(netrole.Observation{Peers: 0}); got != netrole.ActionNone {
		t.Fatalf("timer did not reset: %v", got)
	}
}

// UT-19: a unit that is legitimately alone in a fleet of three must not
// demote on a fixed cycle forever — each fruitless heal backs the next one
// off, up to the cap, so a switched-off peer costs at most a few brief
// interruptions rather than one every 90 s indefinitely.
func TestFruitlessHealBacksOff(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})

	var last time.Duration
	for round := 0; round < 4; round++ {
		waited := demoteAndComeBack(t, m, clk)
		if round > 0 && waited <= last {
			t.Fatalf("round %d waited %v, not longer than the previous %v", round, waited, last)
		}
		last = waited
	}
	if cap := time.Duration(testTimings().MaxHealMult) * testTimings().HealAfter; last > cap+m.StaggerDelay()+time.Minute {
		t.Fatalf("backoff %v exceeded the cap %v", last, cap)
	}
}

// demoteAndComeBack drives one full fruitless heal cycle — short fleet, then
// demote, then a scan that finds nothing so the unit reclaims the AP — and
// reports how long the short-fleet tolerance was this time round.
func demoteAndComeBack(t *testing.T, m *netrole.Machine, clk *synth.FakeClock) time.Duration {
	t.Helper()
	start := clk.Now()
	for i := 0; i < 1000; i++ {
		if got := m.Step(netrole.Observation{Peers: 0}); got == netrole.ActionDemote {
			waited := clk.Now().Sub(start)
			// Back up as AP: nothing else is on the air.
			for j := 0; j < 1000; j++ {
				if m.Step(netrole.Observation{}) == netrole.ActionBecomeAP {
					return waited
				}
				clk.Advance(5 * time.Second)
			}
			t.Fatal("never came back up as AP")
		}
		clk.Advance(5 * time.Second)
	}
	t.Fatal("never demoted")
	return 0
}

// UT-19: a host still serving most of its fleet never yields, however long it
// runs short. Yielding is decided by majority rather than by whichever host
// runs out of patience first, because those two timers start at unrelated
// moments — a host that had been short for a while could otherwise yield
// ahead of a genuinely stranded one and drop a working network for nothing.
//
// In a fleet of three this is also the "one unit is switched off" case: the
// host serving the remaining mirror holds a majority, so the room never pays
// for an outage that could not have helped.
func TestHostServingAMajorityNeverYields(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})

	// One of two expected members is gone: short, but still a majority.
	for i := 0; i < 400; i++ {
		if got := m.Step(netrole.Observation{Peers: 1}); got != netrole.ActionNone {
			t.Fatalf("step %d = %v, want %v — a majority host must hold its network", i, got, netrole.ActionNone)
		}
		clk.Advance(10 * time.Second)
	}
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want primary", m.Role())
	}

	// Lose the last one too and it becomes a minority, so it may yield.
	deadline := 0
	for ; deadline < 400; deadline++ {
		if m.Step(netrole.Observation{Peers: 0}) == netrole.ActionDemote {
			break
		}
		clk.Advance(10 * time.Second)
	}
	if deadline == 400 {
		t.Fatal("a host serving nobody never yielded")
	}
}

// UT-19: after demoting, a primary that finds another network joins it —
// this is the step that actually collapses a split brain back to one AP.
func TestDemotedPrimaryJoinsTheOtherNetwork(t *testing.T) {
	m, clk := newMachine(t, "a1", 3)
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	m.Step(netrole.Observation{})

	for i := 0; i < 1000; i++ {
		if m.Step(netrole.Observation{Peers: 0}) == netrole.ActionDemote {
			break
		}
		clk.Advance(5 * time.Second)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("setup: role = %v, want %v", m.Role(), netrole.RoleUnknown)
	}

	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("after demote with the other AP visible = %v, want %v", got, netrole.ActionJoin)
	}
	if m.Role() != netrole.RoleMember {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RoleMember)
	}
}

// UT-19: production defaults must be sane relative to each other, since the
// E2E lane runs with compressed values and would not catch an inversion.
func TestDefaultTimingsAreOrdered(t *testing.T) {
	d := netrole.DefaultTimings()
	if d.Stagger <= 0 || d.PromoteStep <= 0 || d.HealAfter <= 0 || d.JoinTimeout <= 0 {
		t.Fatalf("non-positive timing in %+v", d)
	}
	if d.HealAfter <= d.PromoteStep {
		t.Fatalf("HealAfter %v must exceed PromoteStep %v, or a promotion race looks like a split brain", d.HealAfter, d.PromoteStep)
	}
	if d.MaxHealMult < 1 {
		t.Fatalf("MaxHealMult = %d, want ≥ 1", d.MaxHealMult)
	}
}
