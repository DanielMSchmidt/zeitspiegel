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
		Stagger:      10 * time.Second,
		StaggerSlots: 4,
		PromoteStep:  20 * time.Second,
		HealAfter:    90 * time.Second,
		JoinTimeout:  30 * time.Second,
		MaxHealMult:  8,
	}
}

func newMachine(t *testing.T, id string) (*netrole.Machine, *synth.FakeClock) {
	t.Helper()
	clk := synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	return netrole.New(id, testTimings(), clk), clk
}

// becomeHost drives a fresh machine through its stagger to hosting.
func becomeHost(t *testing.T, m *netrole.Machine, clk *synth.FakeClock) {
	t.Helper()
	m.Step(netrole.Observation{})
	clk.Advance(m.StaggerDelay() + time.Second)
	if got := m.Step(netrole.Observation{}); got != netrole.ActionBecomeAP {
		t.Fatalf("setup: expected to claim the AP, got %v", got)
	}
}

// UT-19: the shared SSID is already on the air ⇒ join it, never claim it.
// This is the whole "add devices step by step" story: a unit powered on later
// finds the network and becomes part of it, no configuration involved.
func TestSSIDSeenJoins(t *testing.T) {
	m, _ := newMachine(t, "a1")

	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("step = %v, want %v", got, netrole.ActionJoin)
	}
	if m.Role() != netrole.RoleMember {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RoleMember)
	}
}

// UT-19: with nothing on the air a unit waits out its short id-derived
// stagger slot before claiming, rescanning throughout. There is no fleet
// size, so the slot count is fixed — and small, because a solo dancer's
// Wi-Fi must not take a minute to appear.
func TestStaggerBeforeClaimingAP(t *testing.T) {
	m, clk := newMachine(t, "a1")

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

// UT-19: the stagger is bounded by the fixed slot count, not by any fleet
// notion — a solo unit's network appears within seconds of process start.
func TestStaggerIsShortAndBounded(t *testing.T) {
	tm := testTimings()
	max := time.Duration(tm.StaggerSlots) * tm.Stagger
	for _, id := range []string{"a1", "b2", "c3", "ffffff", "zz"} {
		m, _ := newMachine(t, id)
		if d := m.StaggerDelay(); d < 0 || d >= max {
			t.Errorf("id %q stagger %v outside [0, %v)", id, d, max)
		}
	}
	prod := netrole.DefaultTimings()
	if worst := time.Duration(prod.StaggerSlots) * prod.Stagger; worst > 15*time.Second {
		t.Errorf("production worst-case stagger %v: a solo unit's Wi-Fi must be up within seconds", worst)
	}
}

// UT-19: once the stagger expires with still nothing on the air, claim it.
func TestClaimsAPAfterStagger(t *testing.T) {
	m, clk := newMachine(t, "a1")
	becomeHost(t, m, clk)
	if m.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RolePrimary)
	}
}

// UT-19: a scan refused because this unit is itself beaconing must never be
// read as "nobody is out there" — that is exactly how a split brain would be
// created. It is handled, not fatal (ARCHITECTURE D8).
func TestRefusedScanNeverClaimsAP(t *testing.T) {
	m, clk := newMachine(t, "a1")
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
	m, clk := newMachine(t, "a1")
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
	m, clk := newMachine(t, "a1")
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

// UT-19: when the host disappears the surviving members promote in roster
// order — the lowest surviving id first, the next covering automatically if
// that unit is dead too. Unchanged from the fixed-fleet design; the roster
// comes from registration, not configuration.
func TestPromotionOrderFollowsRosterPosition(t *testing.T) {
	roster := []string{"a1", "b2", "c3"} // a1 was hosting

	for _, tc := range []struct {
		id   string
		want time.Duration
	}{
		{"b2", 1 * testTimings().PromoteStep},
		{"c3", 2 * testTimings().PromoteStep},
	} {
		m, clk := newMachine(t, tc.id)
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

// UT-19: a member waiting its promotion turn that sees the SSID reappear
// joins immediately instead of sitting out its whole slot. Rejoining can
// never create a second AP, so it is always safe — and it is what makes a
// transient Wi-Fi blip cost seconds rather than most of a minute.
func TestPendingPromoteJoinsWhenSSIDReappears(t *testing.T) {
	m, clk := newMachine(t, "c3")
	m.SetRoster([]string{"a1", "b2", "c3"})
	m.Step(netrole.Observation{SSIDSeen: true})
	m.Step(netrole.Observation{Associated: true})
	m.Step(netrole.Observation{Associated: false}) // blip

	// Long before c3's promotion turn (2 × PromoteStep), the AP is visible
	// again — either it never really died, or a faster survivor promoted.
	clk.Advance(time.Second)
	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("SSID back during promote wait = %v, want %v", got, netrole.ActionJoin)
	}
	if m.Role() != netrole.RoleMember {
		t.Fatalf("role = %v, want %v", m.Role(), netrole.RoleMember)
	}
}

// UT-19: the promote wait still expires into a search when the SSID stays
// gone, and finding it at that point joins rather than claims.
func TestLosingAPThenSeeingItAgainRejoins(t *testing.T) {
	m, clk := newMachine(t, "c3")
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

// UT-19: the heart of the dynamic design — a host serving ANYBODY holds its
// network forever. Registered units and dancers' phones count equally: a
// mirror with an audience never takes the Wi-Fi away from it.
func TestHostServingAnyoneNeverProbes(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  netrole.Observation
	}{
		{"a registered unit", netrole.Observation{Peers: 1}},
		{"just a phone", netrole.Observation{Stations: 1}},
		{"both", netrole.Observation{Peers: 2, Stations: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, clk := newMachine(t, "a1")
			becomeHost(t, m, clk)

			for i := 0; i < 400; i++ {
				if got := m.Step(tc.obs); got != netrole.ActionNone {
					t.Fatalf("step %d = %v, want %v — a host serving anyone must hold", i, got, netrole.ActionNone)
				}
				clk.Advance(time.Minute)
			}
			if m.Role() != netrole.RolePrimary {
				t.Fatalf("role = %v, want primary", m.Role())
			}
		})
	}
}

// UT-19: a host serving nobody at all probes after HealAfter — drop the AP,
// look around, join what it finds or reclaim. With zero stations there is
// nobody to kick, so the probe is free by construction.
func TestIdleHostProbesAfterHealWindow(t *testing.T) {
	m, clk := newMachine(t, "a1")
	becomeHost(t, m, clk)

	if got := m.Step(netrole.Observation{}); got != netrole.ActionNone {
		t.Fatalf("first idle observation = %v, want %v", got, netrole.ActionNone)
	}
	clk.Advance(testTimings().HealAfter / 2)
	if got := m.Step(netrole.Observation{}); got != netrole.ActionNone {
		t.Fatalf("half-way = %v, want %v", got, netrole.ActionNone)
	}

	clk.Advance(testTimings().HealAfter + m.StaggerDelay() + time.Second)
	if got := m.Step(netrole.Observation{}); got != netrole.ActionDemote {
		t.Fatalf("after the heal window = %v, want %v", got, netrole.ActionDemote)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("role after probe = %v, want %v", m.Role(), netrole.RoleUnknown)
	}
}

// UT-19: anyone showing up resets the probe timer AND the backoff — a phone
// connecting is proof the network is in use.
func TestAudienceResetsProbeTimerAndBackoff(t *testing.T) {
	m, clk := newMachine(t, "a1")
	becomeHost(t, m, clk)

	// Run one fruitless probe so the backoff is above its floor.
	firstWait := probeAndReclaim(t, m, clk)

	// A phone connects briefly, then leaves.
	m.Step(netrole.Observation{Stations: 1})

	// The next probe must take the base wait again, not the backed-off one.
	secondWait := probeAndReclaim(t, m, clk)
	if secondWait > firstWait {
		t.Fatalf("wait after an audience visit %v, want at most the base %v — backoff must reset", secondWait, firstWait)
	}
}

// UT-19: each fruitless probe backs the next one off, up to the cap, so a
// genuinely lonely unit converges to a quiet cadence instead of flapping.
func TestFruitlessProbeBacksOff(t *testing.T) {
	m, clk := newMachine(t, "a1")
	becomeHost(t, m, clk)

	var last time.Duration
	for round := 0; round < 4; round++ {
		waited := probeAndReclaim(t, m, clk)
		if round > 0 && waited <= last {
			t.Fatalf("round %d waited %v, not longer than the previous %v", round, waited, last)
		}
		last = waited
	}
	tm := testTimings()
	if cap := time.Duration(tm.MaxHealMult)*tm.HealAfter + m.StaggerDelay(); last > cap+time.Minute {
		t.Fatalf("backoff %v exceeded the cap %v", last, cap)
	}
}

// probeAndReclaim drives one fruitless probe cycle — idle until Demote, then
// scans that find nothing until the unit reclaims the AP — and reports how
// long the host tolerated being idle this round.
func probeAndReclaim(t *testing.T, m *netrole.Machine, clk *synth.FakeClock) time.Duration {
	t.Helper()
	start := clk.Now()
	for i := 0; i < 2000; i++ {
		if got := m.Step(netrole.Observation{}); got == netrole.ActionDemote {
			waited := clk.Now().Sub(start)
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
	t.Fatal("never probed")
	return 0
}

// UT-19: a probing host that finds another network joins it — this is what
// collapses a cold-boot 0+0 split back to one AP.
func TestProbingHostJoinsWhatItFinds(t *testing.T) {
	m, clk := newMachine(t, "a1")
	becomeHost(t, m, clk)

	for i := 0; i < 2000; i++ {
		if m.Step(netrole.Observation{}) == netrole.ActionDemote {
			break
		}
		clk.Advance(5 * time.Second)
	}
	if m.Role() != netrole.RoleUnknown {
		t.Fatalf("setup: role = %v, want %v", m.Role(), netrole.RoleUnknown)
	}

	if got := m.Step(netrole.Observation{SSIDSeen: true}); got != netrole.ActionJoin {
		t.Fatalf("after probe with the other AP visible = %v, want %v", got, netrole.ActionJoin)
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
	if d.StaggerSlots < 2 {
		t.Fatalf("StaggerSlots = %d: without slots the stagger separates nothing", d.StaggerSlots)
	}
	if d.HealAfter <= d.PromoteStep {
		t.Fatalf("HealAfter %v must exceed PromoteStep %v, or a promotion race looks like a split brain", d.HealAfter, d.PromoteStep)
	}
	if d.MaxHealMult < 1 {
		t.Fatalf("MaxHealMult = %d, want ≥ 1", d.MaxHealMult)
	}
}
