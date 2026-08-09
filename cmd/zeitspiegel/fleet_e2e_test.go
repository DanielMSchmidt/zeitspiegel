//go:build e2e

// Multi-unit end-to-end suite (ST-7..ST-12): three real zeitspiegel
// processes, each with its own engine, its own delay and its own HTTP
// server, electing a role among themselves over real HTTP.
//
// The only thing faked is the radio. --net-sim points every unit at a shared
// directory that behaves like the air does in the ways that decide the
// design: a unit that is beaconing cannot scan, two units can beacon the same
// SSID while blind to each other, and a beacon that stops being refreshed
// goes off the air — so SIGKILL here is exactly pulling a plug.
//
// No ffmpeg, no radios, no root: this runs on a laptop. `make test-e2e`.
package main_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fleetSize = 3
	// netScale compresses every election timing by this factor, so a whole
	// failover happens in seconds instead of a minute. Production runs at 1.
	netScale = 30
	// settleFor bounds how long a test waits for the fleet to reach a state.
	settleFor = 25 * time.Second
)

var (
	buildOnce sync.Once
	binPath   string
	buildDir  string
	buildErr  error
)

// binary builds the real appliance once for the whole suite.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "zeitspiegel-e2e")
		if buildErr != nil {
			return
		}
		binPath = filepath.Join(buildDir, "zeitspiegel")
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// --- one unit ----------------------------------------------------------------

type unit struct {
	t       *testing.T
	id      string
	name    string
	port    int
	base    string
	air     string
	logPath string
	size    int // fleet_size this unit is configured with
	cmd     *exec.Cmd
	log     *os.File
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startUnit launches one real appliance and waits for it to serve.
func startUnit(t *testing.T, id, name, air string) *unit {
	return startUnitOfSize(t, id, name, air, fleetSize)
}

// startUnitOfSize is startUnit with an explicit fleet_size, so the
// single-appliance case can be exercised through the same harness.
func startUnitOfSize(t *testing.T, id, name, air string, size int) *unit {
	t.Helper()
	// t.TempDir() mints a fresh directory on every call, so the log path is
	// fixed once here — a restarted unit must append to the same file.
	u := &unit{t: t, id: id, name: name, port: freeTCPPort(t), air: air, size: size,
		logPath: filepath.Join(t.TempDir(), id+".log")}
	u.base = fmt.Sprintf("http://127.0.0.1:%d", u.port)
	u.start()
	t.Cleanup(u.kill)
	return u
}

func (u *unit) start() {
	u.t.Helper()
	log, err := os.OpenFile(u.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		u.t.Fatal(err)
	}
	u.log = log

	u.cmd = exec.Command(binary(u.t),
		"--source", "synth",
		"--bind", fmt.Sprintf("127.0.0.1:%d", u.port),
		"--unit-id", u.id,
		"--unit-name", u.name,
		"--fleet-size", fmt.Sprint(u.size),
		"--net-sim", u.air,
		"--net-scale", fmt.Sprint(netScale),
	)
	u.cmd.Stdout, u.cmd.Stderr = log, log
	if err := u.cmd.Start(); err != nil {
		u.t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(u.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("unit %s not healthy within 20s (last err %v); log:\n%s", u.id, err, u.tail())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// kill pulls the plug: SIGKILL, so the unit gets no chance to tidy up and its
// beacon simply stops being refreshed.
func (u *unit) kill() {
	if u.cmd == nil || u.cmd.Process == nil {
		return
	}
	u.cmd.Process.Kill()
	u.cmd.Wait()
	u.cmd = nil
	if u.log != nil {
		u.log.Close()
		u.log = nil
	}
}

func (u *unit) tail() string {
	b, err := os.ReadFile(u.logPath)
	if err != nil {
		return "(no log)"
	}
	if len(b) > 4000 {
		b = b[len(b)-4000:]
	}
	return string(b)
}

type status struct {
	DelayS    float64 `json:"delay_s"`
	UnitID    string  `json:"unit_id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	FleetSize int     `json:"fleet_size"`
}

func (u *unit) status() (status, error) {
	resp, err := http.Get(u.base + "/api/v1/status")
	if err != nil {
		return status{}, err
	}
	defer resp.Body.Close()
	var st status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return status{}, err
	}
	return st, nil
}

func (u *unit) mustStatus() status {
	u.t.Helper()
	st, err := u.status()
	if err != nil {
		u.t.Fatalf("unit %s status: %v", u.id, err)
	}
	return st
}

type peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	AgeS    float64 `json:"age_s"`
}

func (u *unit) peers() []peer {
	u.t.Helper()
	resp, err := http.Get(u.base + "/api/v1/peers")
	if err != nil {
		u.t.Fatalf("unit %s peers: %v", u.id, err)
	}
	defer resp.Body.Close()
	var out struct {
		Peers []peer `json:"peers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		u.t.Fatalf("unit %s peers decode: %v", u.id, err)
	}
	return out.Peers
}

// setDelay drives this unit's slider, exactly as its card on the combined
// page does.
func setDelay(t *testing.T, base string, seconds float64) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/api/v1/delay",
		strings.NewReader(fmt.Sprintf(`{"seconds":%v}`, seconds)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT delay on %s: %v", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT delay on %s = %d", base, resp.StatusCode)
	}
}

// --- fleet-level helpers ------------------------------------------------------

// live returns the units that are still running.
func live(units []*unit) []*unit {
	var out []*unit
	for _, u := range units {
		if u.cmd != nil {
			out = append(out, u)
		}
	}
	return out
}

// primaryOf returns the single unit reporting itself as hosting the network,
// or an error describing why the fleet is not settled.
func primaryOf(units []*unit) (*unit, error) {
	var primaries []*unit
	var roles []string
	for _, u := range live(units) {
		st, err := u.status()
		if err != nil {
			return nil, fmt.Errorf("%s unreachable: %w", u.id, err)
		}
		roles = append(roles, u.id+"="+st.Role)
		switch st.Role {
		case "primary":
			primaries = append(primaries, u)
		case "member":
		default:
			return nil, fmt.Errorf("%s is still %s (roles: %v)", u.id, st.Role, roles)
		}
	}
	if len(primaries) != 1 {
		return nil, fmt.Errorf("%d units hosting the network, want exactly 1 (roles: %v)", len(primaries), roles)
	}
	return primaries[0], nil
}

// waitSettled blocks until exactly one live unit hosts the network and every
// other live unit has joined it.
func waitSettled(t *testing.T, units []*unit, what string) *unit {
	t.Helper()
	deadline := time.Now().Add(settleFor)
	var last error
	for time.Now().Before(deadline) {
		p, err := primaryOf(units)
		if err == nil {
			return p
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: fleet never settled within %v: %v", what, settleFor, last)
	return nil
}

// waitPeers blocks until the primary lists exactly n members.
func waitPeers(t *testing.T, primary *unit, n int, what string) []peer {
	t.Helper()
	deadline := time.Now().Add(settleFor)
	var got []peer
	for time.Now().Before(deadline) {
		got = primary.peers()
		if len(got) == n {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: primary %s lists %d members, want %d", what, primary.id, len(got), n)
	return nil
}

func startFleet(t *testing.T, n int) ([]*unit, string) {
	t.Helper()
	air := filepath.Join(t.TempDir(), "air")
	if err := os.MkdirAll(air, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"Barre", "Window", "Corner"}
	var units []*unit
	for i := 0; i < n; i++ {
		units = append(units, startUnit(t, fmt.Sprintf("unit-%d", i+1), names[i%len(names)], air))
	}
	return units, air
}

// beaconing reports the unit ids currently hosting a network in this
// airspace, ignoring beacons that have gone stale because their process died.
func beaconing(t *testing.T, air string) []string {
	t.Helper()
	entries, err := os.ReadDir(air)
	if err != nil {
		t.Fatalf("read airspace: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ap") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) > 2*time.Second {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".ap"))
	}
	return out
}

// ST-7: a lone appliance still brings its own network up. Both NetworkManager
// profiles ship with autoconnect=false so they cannot race the election, which
// makes the binary the only thing that ever hosts a network — including when
// there is no fleet at all. Skipping the election for fleet_size 1 would leave
// a single appliance with no Wi-Fi and no way in.
func TestLoneUnitStillHostsItsOwnNetwork(t *testing.T) {
	air := filepath.Join(t.TempDir(), "air")
	if err := os.MkdirAll(air, 0o755); err != nil {
		t.Fatal(err)
	}
	u := startUnitOfSize(t, "solo", "Solo", air, 1)

	deadline := time.Now().Add(settleFor)
	for {
		if hosts := beaconing(t, air); len(hosts) == 1 && hosts[0] == "solo" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lone unit never brought its network up; log:\n%s", u.tail())
		}
		time.Sleep(100 * time.Millisecond)
	}

	st := u.mustStatus()
	if st.Role != "primary" || st.FleetSize != 1 {
		t.Fatalf("lone unit reports %+v, want primary in a fleet of 1", st)
	}
	// And it serves no fleet API: there is no fleet to describe.
	resp, err := http.Get(u.base + "/api/v1/peers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("GET /api/v1/peers on a lone unit = %d, want 404", resp.StatusCode)
	}
}

// --- ST-7 --------------------------------------------------------------------

// ST-7: three identical appliances, powered on together, sort themselves out.
// Nothing designates a primary; they all run the same binary with the same
// flags apart from an id, which on real hardware comes from the CPU serial.
func TestFleetColdStartElectsOnePrimary(t *testing.T) {
	units, _ := startFleet(t, 3)

	primary := waitSettled(t, units, "cold start")
	list := waitPeers(t, primary, 2, "cold start")

	seen := map[string]string{}
	for _, p := range list {
		seen[p.ID] = p.Name
		if !strings.HasPrefix(p.BaseURL, "http://127.0.0.1:") {
			t.Errorf("peer %s base_url = %q, want an address derived from the connection", p.ID, p.BaseURL)
		}
	}
	for _, u := range units {
		if u.id == primary.id {
			continue
		}
		if got := seen[u.id]; got != u.name {
			t.Errorf("primary lists %s as %q, want %q", u.id, got, u.name)
		}
	}

	// Every unit knows its own identity and the size of the fleet, which is
	// what the combined page labels its cards with.
	for _, u := range units {
		st := u.mustStatus()
		if st.UnitID != u.id || st.Name != u.name || st.FleetSize != fleetSize {
			t.Errorf("unit %s reports %+v", u.id, st)
		}
	}
}

// --- ST-8 --------------------------------------------------------------------

// ST-8: the requirement, end to end. Three appliances, three delays, each
// settable on its own — and the member sliders are driven through the
// addresses the primary handed out, which is exactly what the combined page
// does.
func TestFleetIndependentDelays(t *testing.T) {
	units, _ := startFleet(t, 3)
	primary := waitSettled(t, units, "cold start")
	list := waitPeers(t, primary, 2, "cold start")

	byID := map[string]*unit{}
	for _, u := range units {
		byID[u.id] = u
	}

	want := map[string]float64{primary.id: 3}
	setDelay(t, primary.base, 3)
	for i, p := range list {
		secs := float64(12 + i*13) // 12, 25
		setDelay(t, p.BaseURL, secs)
		want[p.ID] = secs
	}

	for id, secs := range want {
		if got := byID[id].mustStatus().DelayS; got != secs {
			t.Errorf("unit %s delay = %v, want %v", id, got, secs)
		}
	}

	// Move one again: nobody else may budge.
	setDelay(t, list[0].BaseURL, 7)
	want[list[0].ID] = 7
	for id, secs := range want {
		if got := byID[id].mustStatus().DelayS; got != secs {
			t.Errorf("after moving one slider, unit %s delay = %v, want %v", id, got, secs)
		}
	}
}

// --- ST-9 --------------------------------------------------------------------

// ST-9: pull the primary's plug. Exactly one survivor takes over and the
// other joins it — with no successor designated in advance, so it works
// whichever unit died.
func TestFleetFailoverAfterPrimaryDies(t *testing.T) {
	units, _ := startFleet(t, 3)
	primary := waitSettled(t, units, "cold start")
	waitPeers(t, primary, 2, "cold start")

	primary.kill()

	survivor := waitSettled(t, units, "after the primary died")
	if survivor.id == primary.id {
		t.Fatalf("the killed unit %s is still primary", primary.id)
	}
	waitPeers(t, survivor, 1, "after failover")
}

// --- ST-10 -------------------------------------------------------------------

// ST-10: the ex-primary comes back. It must rejoin as an ordinary member
// rather than taking the network back — a handback would cost the room a
// second outage for nothing. The fleet is watched throughout, so a moment
// with two primaries would fail the test even if the end state looked fine.
func TestFleetExPrimaryRejoinsWithoutFlapping(t *testing.T) {
	units, _ := startFleet(t, 3)
	old := waitSettled(t, units, "cold start")
	waitPeers(t, old, 2, "cold start")

	old.kill()
	newPrimary := waitSettled(t, units, "after failover")

	old.start() // plugged back in: a fresh boot

	// Watch while it settles: never more than one unit hosting the network.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, u := range live(units) {
			if st, err := u.status(); err == nil && st.Role == "primary" {
				n++
			}
		}
		if n > 1 {
			t.Fatalf("%d units hosting the network at once — the returning unit preempted", n)
		}
		time.Sleep(100 * time.Millisecond)
	}

	settled := waitSettled(t, units, "after the ex-primary returned")
	if settled.id != newPrimary.id {
		t.Fatalf("primary changed to %s; the returning unit must not preempt %s", settled.id, newPrimary.id)
	}
	if st := old.mustStatus(); st.Role != "member" {
		t.Fatalf("returned unit %s is %q, want member", old.id, st.Role)
	}
	waitPeers(t, newPrimary, 2, "after the ex-primary rejoined")
}

// --- ST-11 -------------------------------------------------------------------

// ST-11: the failure no scan can detect. Two units come up on separate
// stretches of air, each hosting the same SSID, each blind to the other
// because a beaconing radio cannot scan. When the air merges, the fleet has
// to notice from its own membership count alone and collapse back to one
// network.
//
// The merge is done by repointing a symlink, so the running processes see it
// on their next scan without being restarted or reconfigured.
func TestFleetSplitBrainSelfHeals(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	isolated := filepath.Join(root, "isolated")
	for _, d := range []string{shared, isolated} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Each unit reaches its air through a symlink we can repoint later.
	airA, airB := filepath.Join(root, "air-a"), filepath.Join(root, "air-b")
	if err := os.Symlink(shared, airA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(isolated, airB); err != nil {
		t.Fatal(err)
	}

	a := startUnit(t, "unit-1", "Barre", airA)
	b := startUnit(t, "unit-2", "Window", airB)
	units := []*unit{a, b}

	// Both alone, so both host: a split brain by construction.
	deadline := time.Now().Add(settleFor)
	for {
		sa, errA := a.status()
		sb, errB := b.status()
		if errA == nil && errB == nil && sa.Role == "primary" && sb.Role == "primary" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: never reached two primaries (%+v %+v)", sa, sb)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// One stretch of air again. Neither unit can see the other — both are
	// beaconing — so recovery has to come from the membership count.
	if err := os.Remove(airB); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, airB); err != nil {
		t.Fatal(err)
	}

	healed := waitSettled(t, units, "after the split brain")
	waitPeers(t, healed, 1, "after the split brain healed")
}

// --- ST-12 -------------------------------------------------------------------

// ST-12: the clip endpoint on a member answers on the member's own address —
// the one the primary handed out — so a download comes straight from the unit
// holding the footage instead of being relayed through the primary onto a
// second wireless hop.
//
// The validation half needs no encoder: seconds=0 is rejected before ffmpeg
// is ever invoked, which proves the route and the handler are live on that
// address. The download half needs a real encoder and skips without one; the
// MP4's validity is IT-3's job in any case.
func TestFleetClipEndpointOnMemberAddress(t *testing.T) {
	units, _ := startFleet(t, 3)
	primary := waitSettled(t, units, "cold start")
	list := waitPeers(t, primary, 2, "cold start")
	member := list[0]

	resp, err := http.Get(member.BaseURL + "/api/v1/clip?seconds=0")
	if err != nil {
		t.Fatalf("GET clip from member %s: %v", member.ID, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("clip?seconds=0 on member = %d, want 422 (FR-11)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed: skipping the download half (IT-3 covers MP4 validity)")
	}

	// Give the member a moment of footage to cut from.
	time.Sleep(2 * time.Second)
	dl, err := http.Get(member.BaseURL + "/api/v1/clip?seconds=1")
	if err != nil {
		t.Fatalf("GET clip from member %s: %v", member.ID, err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != 200 {
		t.Fatalf("clip from member = %d, want 200", dl.StatusCode)
	}
	if ct := dl.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", ct)
	}
	if dl.Header.Get("X-Clip-Duration") == "" {
		t.Error("X-Clip-Duration missing: the duration must be known before the body streams")
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}

// ST-12: the combined page is served by the primary but talks straight to
// every unit, so each unit has to answer a cross-origin call and its
// preflight. Without this a peer's delay slider silently does nothing.
func TestFleetPeersAnswerCrossOriginCalls(t *testing.T) {
	units, _ := startFleet(t, 3)
	primary := waitSettled(t, units, "cold start")
	list := waitPeers(t, primary, 2, "cold start")

	for _, p := range list {
		req, err := http.NewRequest(http.MethodOptions, p.BaseURL+"/api/v1/delay", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", primary.base)
		req.Header.Set("Access-Control-Request-Method", "PUT")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("preflight to %s: %v", p.ID, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("peer %s preflight = %d, want 204", p.ID, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("peer %s Allow-Origin = %q", p.ID, got)
		}
	}
}
