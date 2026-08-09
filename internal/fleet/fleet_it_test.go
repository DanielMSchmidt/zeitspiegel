package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/fleet"
	"github.com/danielmschmidt/zeitspiegel/internal/netrole"
	"github.com/danielmschmidt/zeitspiegel/internal/peers"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

const (
	ssid      = "zeitspiegel"
	roundStep = 5 * time.Second
	peerTTL   = 30 * time.Second
)

func timings() netrole.Timings {
	return netrole.Timings{
		Stagger:     10 * time.Second,
		PromoteStep: 20 * time.Second,
		HealAfter:   90 * time.Second,
		JoinTimeout: 30 * time.Second,
		MaxHealMult: 8,
	}
}

// --- a virtual airspace ------------------------------------------------------
//
// Faithful in the one way that matters: a unit that is beaconing cannot
// scan. Two units can beacon the same SSID at once and neither can see the
// other, which is exactly the split brain the real hardware permits and the
// reason the self-heal exists.

type airspace struct {
	mu  sync.Mutex
	aps map[string]int // beaconing unit id → partition
}

func newAirspace() *airspace { return &airspace{aps: map[string]int{}} }

// apIn returns a unit beaconing in the given partition. When more than one is
// (a split brain), the lowest id is returned rather than a random map entry,
// so a test that reaches that state is still deterministic.
func (a *airspace) apIn(partition int, excluding string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	best, found := "", false
	for id, p := range a.aps {
		if p != partition || id == excluding {
			continue
		}
		if !found || id < best {
			best, found = id, true
		}
	}
	return best, found
}

// beaconing reports whether a specific unit is on the air in a partition.
func (a *airspace) beaconing(id string, partition int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.aps[id]
	return ok && p == partition
}

func (a *airspace) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.aps)
}

func (a *airspace) ids() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.aps))
	for id := range a.aps {
		out = append(out, id)
	}
	return out
}

// merge drops every partition boundary, putting all units on one channel —
// how a split brain becomes visible to the fleet's own recovery.
func (a *airspace) merge() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.aps {
		a.aps[id] = 0
	}
}

type fakeRadio struct {
	air       *airspace
	id        string
	partition int
	mode      string // "off", "ap", "sta"
	// assoc is the unit this radio associated with. A real station stays on
	// the access point it joined; it does not re-pick one every scan. Without
	// that stickiness a member would drift between two hosts during a split
	// brain, which no real radio does and which would make the fleet's
	// membership counts meaningless.
	assoc string
}

func (r *fakeRadio) Scan(context.Context) ([]string, error) {
	if r.mode == "ap" {
		return nil, fleet.ErrScanUnavailable
	}
	if _, ok := r.air.apIn(r.partition, r.id); ok {
		return []string{ssid}, nil
	}
	return nil, nil
}

func (r *fakeRadio) ActivateAP(context.Context) error {
	r.air.mu.Lock()
	defer r.air.mu.Unlock()
	r.air.aps[r.id] = r.partition
	r.mode, r.assoc = "ap", ""
	return nil
}

func (r *fakeRadio) ActivateSTA(context.Context) error {
	r.air.mu.Lock()
	delete(r.air.aps, r.id)
	r.air.mu.Unlock()
	r.mode = "sta"
	// Pick an access point now and stay on it.
	r.assoc, _ = r.air.apIn(r.partition, r.id)
	return nil
}

func (r *fakeRadio) Down(context.Context) error {
	r.air.mu.Lock()
	delete(r.air.aps, r.id)
	r.air.mu.Unlock()
	r.mode, r.assoc = "off", ""
	return nil
}

func (r *fakeRadio) Associated(context.Context) (bool, error) {
	if r.mode != "sta" || r.assoc == "" {
		return false, nil
	}
	return r.air.beaconing(r.assoc, r.partition), nil
}

func (r *fakeRadio) Gateway(context.Context) (string, error) {
	if r.mode != "sta" || r.assoc == "" || !r.air.beaconing(r.assoc, r.partition) {
		return "", errors.New("no gateway: not associated")
	}
	return "http://" + r.assoc, nil
}

// --- harness -----------------------------------------------------------------

type unit struct {
	id    string
	radio *fakeRadio
	reg   *peers.Registry
	mach  *netrole.Machine
	sup   *fleet.Supervisor
	alive bool
}

type harness struct {
	t     *testing.T
	clk   *synth.FakeClock
	air   *airspace
	units []*unit
	fleet int
}

func newHarness(t *testing.T, ids []string) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		clk:   synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)),
		air:   newAirspace(),
		fleet: len(ids),
	}
	for _, id := range ids {
		h.units = append(h.units, h.build(id, 0))
	}
	return h
}

func (h *harness) build(id string, partition int) *unit {
	u := &unit{
		id:    id,
		radio: &fakeRadio{air: h.air, id: id, partition: partition},
		reg:   peers.NewRegistry(id, peerTTL, h.clk),
		mach:  netrole.New(id, h.fleet, timings(), h.clk),
		alive: true,
	}
	u.sup = &fleet.Supervisor{
		SSID:          ssid,
		Machine:       u.mach,
		Radio:         u.radio,
		Peers:         u.reg,
		Clock:         h.clk,
		AnnounceEvery: 10 * time.Second,
		Announce:      func(ctx context.Context) error { return h.announce(u) },
		OnError:       func(error) {}, // failures are expected while the fleet is unsettled
	}
	return u
}

// announce mimics a member registering with whichever unit is currently
// hosting its network, and feeding the roster it gets back into its own
// election machine — the same loop the real Announcer runs over HTTP.
func (h *harness) announce(u *unit) error {
	// Announce to the access point this unit is actually associated with —
	// its default route — not to whichever one happens to be on the air.
	apID := u.radio.assoc
	if apID == "" || !h.air.beaconing(apID, u.radio.partition) {
		return errors.New("no primary to announce to")
	}
	ap := h.find(apID)
	if ap == nil {
		return errors.New("primary vanished")
	}
	if err := ap.reg.Register(peers.Peer{ID: u.id, Name: u.id, BaseURL: "http://" + u.id}); err != nil {
		return err
	}
	u.mach.SetRoster(ap.reg.Roster())
	return nil
}

func (h *harness) find(id string) *unit {
	for _, u := range h.units {
		if u.id == id {
			return u
		}
	}
	return nil
}

// settle advances the fake clock, ticking every live unit each round.
func (h *harness) settle(rounds int) {
	ctx := context.Background()
	for i := 0; i < rounds; i++ {
		for _, u := range h.units {
			if u.alive {
				u.sup.Tick(ctx)
			}
		}
		h.clk.Advance(roundStep)
	}
}

// kill removes a unit from the air and stops driving it — pulling the plug.
func (h *harness) kill(id string) {
	u := h.find(id)
	if u == nil {
		h.t.Fatalf("kill: no unit %q", id)
	}
	u.radio.Down(context.Background())
	u.alive = false
}

func (h *harness) roles() map[string]netrole.Role {
	out := map[string]netrole.Role{}
	for _, u := range h.units {
		if u.alive {
			out[u.id] = u.mach.Role()
		}
	}
	return out
}

// requireOneAP is the fleet's central invariant: exactly one unit beaconing,
// every other live unit settled as a member of it.
func (h *harness) requireOneAP(what string) string {
	h.t.Helper()
	if n := h.air.count(); n != 1 {
		h.t.Fatalf("%s: %d units beaconing (%v), want exactly 1; roles %v",
			what, n, h.air.ids(), h.roles())
	}
	primary := h.air.ids()[0]
	for id, role := range h.roles() {
		want := netrole.RoleMember
		if id == primary {
			want = netrole.RolePrimary
		}
		if role != want {
			h.t.Fatalf("%s: unit %q is %v, want %v (primary is %q)", what, id, role, want, primary)
		}
	}
	return primary
}

// distinctSlotIDs finds ids that land in different stagger slots, so a clean
// cold start can be asserted without depending on how the hash happens to
// spread three arbitrary names.
func distinctSlotIDs(t *testing.T, fleetSize int) []string {
	t.Helper()
	clk := synth.NewFakeClock(time.Now())
	bySlot := map[time.Duration]string{}
	for i := 0; len(bySlot) < fleetSize && i < 10000; i++ {
		id := fmt.Sprintf("u%d", i)
		d := netrole.New(id, fleetSize, timings(), clk).StaggerDelay()
		if _, taken := bySlot[d]; !taken {
			bySlot[d] = id
		}
	}
	if len(bySlot) < fleetSize {
		t.Fatalf("could not find %d ids in distinct stagger slots", fleetSize)
	}
	out := make([]string, 0, fleetSize)
	for _, id := range bySlot {
		out = append(out, id)
	}
	return out
}

// --- IT-11 -------------------------------------------------------------------

// IT-11: three units powered on together settle into exactly one access
// point with the other two joined to it — no coordination, no baked roles,
// identical software on every card.
func TestColdStartElectsOnePrimary(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	primary := h.requireOneAP("cold start")

	// And the primary actually knows about the others.
	if got := h.find(primary).reg.Count(); got != 2 {
		t.Fatalf("primary %q sees %d members, want 2", primary, got)
	}
}

// IT-11: pull the primary's plug and exactly one survivor takes over, with
// the other rejoining it. Nothing designates a successor in advance, so this
// works whichever unit died.
func TestPrimaryFailoverPromotesExactlyOne(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	primary := h.requireOneAP("cold start")

	h.kill(primary)
	h.settle(40)

	survivor := h.requireOneAP("after failover")
	if survivor == primary {
		t.Fatalf("the killed unit %q is still primary", primary)
	}
}

// IT-11: the fleet must survive losing two of three — the rule is "the
// lowest surviving id goes first", not "the second one goes", so a dead
// successor cannot deadlock the remaining unit.
func TestFailoverSurvivesLosingTwoUnits(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	primary := h.requireOneAP("cold start")
	h.kill(primary)
	h.settle(40)

	second := h.requireOneAP("after first failover")
	h.kill(second)
	h.settle(40)

	last := h.requireOneAP("after second failover")
	if last == primary || last == second {
		t.Fatalf("a dead unit (%q) is primary", last)
	}
}

// IT-11: an ex-primary that gets plugged back in rejoins as a member. It
// must not preempt — a handback would cost the room a second outage for no
// benefit — and the fleet must stay on exactly one AP throughout.
func TestExPrimaryReturnsAsMemberWithoutFlapping(t *testing.T) {
	ids := distinctSlotIDs(t, 3)
	h := newHarness(t, ids)
	h.settle(20)
	old := h.requireOneAP("cold start")

	h.kill(old)
	h.settle(40)
	newPrimary := h.requireOneAP("after failover")

	// Plug the old one back in: a fresh boot, so a fresh machine.
	revived := h.build(old, 0)
	for i, u := range h.units {
		if u.id == old {
			h.units[i] = revived
		}
	}

	// Watch the whole settling period, not just the end state: a handback
	// would show up as a moment with two APs or none.
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		for _, u := range h.units {
			if u.alive {
				u.sup.Tick(ctx)
			}
		}
		if n := h.air.count(); n != 1 {
			t.Fatalf("round %d: %d units beaconing (%v), want the network never to drop or double",
				i, n, h.air.ids())
		}
		h.clk.Advance(roundStep)
	}

	if got := h.requireOneAP("after the ex-primary returned"); got != newPrimary {
		t.Fatalf("primary changed to %q; the returning unit should not preempt", got)
	}
	if revived.mach.Role() != netrole.RoleMember {
		t.Fatalf("revived unit is %v, want member", revived.mach.Role())
	}
}

// IT-11: the case no scan can detect. Two units come up on separate
// partitions, each hosting the same SSID and each blind to the other. When
// the partitions merge, the fleet has to notice from its own membership
// count alone and collapse back to one network.
func TestSplitBrainSelfHeals(t *testing.T) {
	h := &harness{
		t:     t,
		clk:   synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)),
		air:   newAirspace(),
		fleet: 2,
	}
	ids := distinctSlotIDs(t, 2)
	h.units = []*unit{h.build(ids[0], 0), h.build(ids[1], 1)} // separate partitions

	h.settle(20)
	if n := h.air.count(); n != 2 {
		t.Fatalf("setup: %d units beaconing, want 2 (a split brain)", n)
	}

	// One channel again. Neither unit can see the other — both are
	// beaconing, and a beaconing radio cannot scan.
	h.air.merge()
	for _, u := range h.units {
		u.radio.partition = 0
	}

	h.settle(120)
	h.requireOneAP("after the split brain healed")
}

// IT-11: the case that looks most like a lease expiring elsewhere. A host is
// cut off from its members without ever restarting, so it keeps beaconing and
// — being in AP mode — cannot see that the others have given up on it and
// elected a new host. It has to work out from its own empty membership list
// that it is the stale one, and rejoin.
//
// Crucially the unit that took over must NOT be the one that yields: it is
// serving a mirror, and dropping its network would cost that mirror an outage
// for nothing.
func TestStrandedHostRejoinsTheNetworkThatReplacedIt(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	stranded := h.requireOneAP("cold start")

	// Cut the old host off without restarting it: it keeps beaconing, blind
	// to everything, while the other two lose their association.
	h.find(stranded).radio.partition = 1
	h.air.mu.Lock()
	h.air.aps[stranded] = 1
	h.air.mu.Unlock()

	h.settle(40)

	// One of the survivors is now hosting on the original stretch of air.
	var replacement string
	for _, u := range h.units {
		if u.id != stranded && u.mach.Role() == netrole.RolePrimary {
			replacement = u.id
		}
	}
	if replacement == "" {
		t.Fatalf("nobody took over after the host was cut off; roles %v", h.roles())
	}

	// Back on one stretch of air: two hosts, the same SSID, neither able to
	// see the other.
	h.find(stranded).radio.partition = 0
	h.air.mu.Lock()
	h.air.aps[stranded] = 0
	h.air.mu.Unlock()
	if h.air.count() != 2 {
		t.Fatalf("setup: %d units beaconing, want 2", h.air.count())
	}

	h.settle(160)

	winner := h.requireOneAP("after the stranded host rejoined")
	if winner != replacement {
		t.Fatalf("network changed hands to %q; the host serving members (%q) must not be the one that yields",
			winner, replacement)
	}
	if role := h.find(stranded).mach.Role(); role != netrole.RoleMember {
		t.Fatalf("stranded host is %v, want member", role)
	}
}

// IT-11: a lone appliance in a fleet of one keeps its network up forever. It
// has no peers to be short of, so the self-heal must never touch it — this
// is the pre-existing single-unit behaviour (E-7) and it must not regress.
func TestSingleUnitFleetKeepsItsNetworkUp(t *testing.T) {
	h := newHarness(t, []string{"solo"})
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		h.units[0].sup.Tick(ctx)
		h.clk.Advance(roundStep)
		if i > 2 && h.air.count() != 1 {
			t.Fatalf("round %d: lone unit dropped its network", i)
		}
	}
	if h.units[0].mach.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want primary", h.units[0].mach.Role())
	}
}

// IT-11: OnRole fires on every transition, which is what cmd hangs the
// transient hostname and the reported role off.
func TestOnRoleFiresOnTransitions(t *testing.T) {
	h := newHarness(t, []string{"solo"})
	var got []netrole.Role
	h.units[0].sup.OnRole = func(r netrole.Role) { got = append(got, r) }

	h.settle(5)
	if len(got) == 0 {
		t.Fatal("OnRole never fired")
	}
	if got[len(got)-1] != netrole.RolePrimary {
		t.Fatalf("last role = %v, want primary (saw %v)", got[len(got)-1], got)
	}
}
