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
		Stagger:      10 * time.Second,
		StaggerSlots: 4,
		PromoteStep:  20 * time.Second,
		HealAfter:    90 * time.Second,
		JoinTimeout:  30 * time.Second,
		MaxHealMult:  8,
	}
}

// --- a virtual airspace ------------------------------------------------------
//
// Faithful in the ways that decide the design: a unit that is beaconing
// cannot scan; two units can beacon the same SSID at once, each blind to the
// other; a station stays on the access point it joined; and an access point
// can count its associated clients without ever scanning — which is the
// signal the whole dynamic election rests on.

type airspace struct {
	mu   sync.Mutex
	aps  map[string]int    // beaconing unit id → partition
	stas map[string]string // associated unit id → the AP it joined
}

func newAirspace() *airspace {
	return &airspace{aps: map[string]int{}, stas: map[string]string{}}
}

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

// unitStations counts the member units associated to the given AP.
func (a *airspace) unitStations(apID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, ap := range a.stas {
		if ap == apID {
			n++
		}
	}
	return n
}

type fakeRadio struct {
	air       *airspace
	id        string
	partition int
	mode      string // "off", "ap", "sta"
	// assoc is the unit this radio associated with. A real station stays on
	// the access point it joined; it does not re-pick one every scan.
	assoc string
	// phantom models guests' phones associated to this unit's AP — clients
	// that are not Zeitspiegel units but count exactly the same for the
	// hold-the-network rule.
	phantom int
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
	r.air.aps[r.id] = r.partition
	delete(r.air.stas, r.id)
	r.air.mu.Unlock()
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
	r.air.mu.Lock()
	if r.assoc != "" {
		r.air.stas[r.id] = r.assoc
	}
	r.air.mu.Unlock()
	return nil
}

func (r *fakeRadio) Down(context.Context) error {
	r.air.mu.Lock()
	delete(r.air.aps, r.id)
	delete(r.air.stas, r.id)
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

func (r *fakeRadio) Stations(context.Context) (int, error) {
	if r.mode != "ap" {
		return 0, nil
	}
	return r.air.unitStations(r.id) + r.phantom, nil
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
}

func newHarness(t *testing.T, ids []string) *harness {
	t.Helper()
	h := &harness{
		t:   t,
		clk: synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)),
		air: newAirspace(),
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
		mach:  netrole.New(id, timings(), h.clk),
		alive: true,
	}
	u.sup = &fleet.Supervisor{
		SSID:          ssid,
		Machine:       u.mach,
		Radio:         u.radio,
		Peers:         u.reg,
		Clock:         h.clk,
		AnnounceEvery: 10 * time.Second,
		StationEvery:  10 * time.Second,
		Announce:      func(ctx context.Context) error { return h.announce(u) },
		OnError:       func(error) {}, // failures are expected while the fleet is unsettled
	}
	return u
}

// announce mimics a member registering with the unit whose network it is
// associated to — its default route — and feeding the roster it gets back
// into its own election machine, the same loop the real Announcer runs over
// HTTP.
func (h *harness) announce(u *unit) error {
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
func distinctSlotIDs(t *testing.T, n int) []string {
	t.Helper()
	clk := synth.NewFakeClock(time.Now())
	bySlot := map[time.Duration]string{}
	for i := 0; len(bySlot) < n && i < 10000; i++ {
		id := fmt.Sprintf("u%d", i)
		d := netrole.New(id, timings(), clk).StaggerDelay()
		if _, taken := bySlot[d]; !taken {
			bySlot[d] = id
		}
	}
	if len(bySlot) < n {
		t.Fatalf("could not find %d ids in distinct stagger slots", n)
	}
	out := make([]string, 0, n)
	for _, id := range bySlot {
		out = append(out, id)
	}
	return out
}

// --- IT-11 -------------------------------------------------------------------

// IT-11: three units powered on together settle into exactly one access
// point with the other two joined to it — no coordination, no baked roles,
// no fleet size, identical software on every card.
func TestColdStartElectsOnePrimary(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	primary := h.requireOneAP("cold start")

	if got := h.find(primary).reg.Count(); got != 2 {
		t.Fatalf("primary %q sees %d members, want 2", primary, got)
	}
}

// IT-11: the studio's core flow — units are added one at a time. The first
// hosts; each later unit finds the network and joins it, whatever the order
// and however long the gaps.
func TestUnitsAddedStepByStepJoin(t *testing.T) {
	ids := distinctSlotIDs(t, 3)
	h := newHarness(t, ids[:1])
	h.settle(10)
	first := h.requireOneAP("first unit alone")

	// Half an hour later, a second unit is powered on.
	h.clk.Advance(30 * time.Minute)
	h.units = append(h.units, h.build(ids[1], 0))
	h.settle(10)
	if got := h.requireOneAP("after the second joined"); got != first {
		t.Fatalf("network changed hands to %q when a unit joined", got)
	}

	// And later a third.
	h.clk.Advance(10 * time.Minute)
	h.units = append(h.units, h.build(ids[2], 0))
	h.settle(10)
	h.requireOneAP("after the third joined")
	if got := h.find(first).reg.Count(); got != 2 {
		t.Fatalf("host sees %d members, want 2", got)
	}
}

// IT-11: pull the host's plug and exactly one survivor takes over, with the
// other rejoining it. Nothing designates a successor in advance.
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

// IT-11: the fleet survives losing two of three — the "lowest surviving id
// goes first" rule cannot deadlock on a dead successor.
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

// IT-11: an ex-host that gets plugged back in rejoins as a member. It must
// not preempt, and the fleet must stay on exactly one AP throughout.
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

	if got := h.requireOneAP("after the ex-host returned"); got != newPrimary {
		t.Fatalf("primary changed to %q; the returning unit should not preempt", got)
	}
	if revived.mach.Role() != netrole.RoleMember {
		t.Fatalf("revived unit is %v, want member", revived.mach.Role())
	}
}

// IT-11: a host is cut off from its members without ever restarting — it
// keeps beaconing, blind to the replacement that got elected behind its
// back. Its members expire from its registry, its station count drops to
// zero, and from that alone it works out that it should look around — while
// the replacement, which is serving a member, holds. The wrong network never
// yields.
func TestStrandedHostRejoinsTheNetworkThatReplacedIt(t *testing.T) {
	h := newHarness(t, distinctSlotIDs(t, 3))
	h.settle(20)
	stranded := h.requireOneAP("cold start")

	// Cut the old host off without restarting it.
	h.find(stranded).radio.partition = 1
	h.air.mu.Lock()
	h.air.aps[stranded] = 1
	h.air.mu.Unlock()

	h.settle(40)

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

	h.settle(200)

	winner := h.requireOneAP("after the stranded host rejoined")
	if winner != replacement {
		t.Fatalf("network changed hands to %q; the host serving a member (%q) must not be the one that yields",
			winner, replacement)
	}
	if role := h.find(stranded).mach.Role(); role != netrole.RoleMember {
		t.Fatalf("stranded host is %v, want member", role)
	}
}

// IT-11: a cold-boot 0+0 split — two units up on separate stretches of air,
// both hosting, neither serving anyone. When the air merges, the first
// prober finds the other network and joins it.
func TestEmptySplitBrainSelfHeals(t *testing.T) {
	h := &harness{
		t:   t,
		clk: synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)),
		air: newAirspace(),
	}
	ids := distinctSlotIDs(t, 2)
	h.units = []*unit{h.build(ids[0], 0), h.build(ids[1], 1)} // separate partitions

	// Long enough for every stagger slot, short of the first idle probe —
	// the point of this test is the split, not the probing.
	h.settle(10)
	if n := h.air.count(); n != 2 {
		t.Fatalf("setup: %d units beaconing, want 2 (a split brain)", n)
	}

	// One channel again. Neither unit can see the other — both are
	// beaconing, and a beaconing radio cannot scan.
	h.air.mu.Lock()
	for id := range h.air.aps {
		h.air.aps[id] = 0
	}
	h.air.mu.Unlock()
	for _, u := range h.units {
		u.radio.partition = 0
	}

	h.settle(250)
	h.requireOneAP("after the split brain healed")
}

// IT-11: the property the whole redesign exists for — a solo host with an
// audience (here: a dancer's phone, modelled as a phantom station) NEVER
// takes its network down, no matter how long it runs and no matter that no
// other Zeitspiegel unit ever appears. This is what was broken by
// fleet_size: a lone unit of a fleet-of-3 image dropped its AP every ~12
// minutes forever.
func TestLoneHostWithAPhoneNeverDropsItsNetwork(t *testing.T) {
	h := newHarness(t, []string{"solo"})
	h.units[0].radio.phantom = 1 // one phone on the AP

	ctx := context.Background()
	// ~2.5 simulated hours — far beyond every heal window and backoff cap.
	for i := 0; i < 1800; i++ {
		h.units[0].sup.Tick(ctx)
		h.clk.Advance(roundStep)
		if i > 3 && h.air.count() != 1 {
			t.Fatalf("round %d (t+%v): the lone host dropped its network with a phone attached",
				i, time.Duration(i)*roundStep)
		}
	}
	if h.units[0].mach.Role() != netrole.RolePrimary {
		t.Fatalf("role = %v, want primary", h.units[0].mach.Role())
	}
}

// IT-11: a solo host with NO audience probes occasionally — and always comes
// back up. The probe is free (nobody attached to kick) and is what heals a
// 0+0 split; a truly lonely unit converges to a quiet, capped cadence.
func TestLoneIdleHostProbesAndAlwaysReturns(t *testing.T) {
	h := newHarness(t, []string{"solo"})

	ctx := context.Background()
	probes, wasDown := 0, false
	for i := 0; i < 1800; i++ {
		h.units[0].sup.Tick(ctx)
		if h.air.count() == 0 {
			wasDown = true
		} else if wasDown {
			wasDown = false
			probes++
		}
		h.clk.Advance(roundStep)
	}
	if probes == 0 {
		t.Fatal("an idle host never probed — a 0+0 split brain would never heal")
	}
	if h.air.count() != 1 {
		t.Fatal("the host did not return after its last probe")
	}
	// The backoff must keep a lonely unit quiet, not flapping: over 2.5
	// simulated hours the capped cadence allows only a handful of probes.
	if probes > 15 {
		t.Fatalf("%d probes in 2.5 h — the backoff is not damping", probes)
	}
}

// IT-11: a phone showing up between probes stops the probing — the audience
// rule works with real arrival times, not just in the pure table test.
func TestPhoneArrivingStopsProbing(t *testing.T) {
	h := newHarness(t, []string{"solo"})
	ctx := context.Background()
	h.settle(8) // past every stagger slot
	if h.air.count() != 1 {
		t.Fatal("setup: host not up")
	}

	// A dancer connects.
	h.units[0].radio.phantom = 1

	for i := 0; i < 1200; i++ {
		h.units[0].sup.Tick(ctx)
		h.clk.Advance(roundStep)
		if i > 3 && h.air.count() != 1 {
			t.Fatalf("round %d: probed despite an attached phone", i)
		}
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
