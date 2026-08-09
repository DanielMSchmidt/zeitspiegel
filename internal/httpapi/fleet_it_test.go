package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
	"github.com/danielmschmidt/zeitspiegel/internal/engine"
	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/peers"
	"github.com/danielmschmidt/zeitspiegel/internal/ringbuf"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

// fleetUnit is one appliance: its own engine, its own delay, its own server.
type fleetUnit struct {
	id  string
	eng *engine.Engine
	reg *peers.Registry
	srv *httptest.Server
}

// unitStatus reports the unit's own view of itself, which is what each card
// on the combined page renders.
func (u *fleetUnit) status(t *testing.T) httpapi.Status {
	t.Helper()
	resp, err := http.Get(u.srv.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st httpapi.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// setDelay drives one unit's slider, exactly as the combined page does.
func setDelay(t *testing.T, base string, seconds float64) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/api/v1/delay",
		strings.NewReader(`{"seconds":`+strconv.FormatFloat(seconds, 'f', -1, 64)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT delay on %s = %d: %s", base, resp.StatusCode, body)
	}
}

// newFleetUnit builds a complete unit. Only a unit that is hosting the
// network gets a registry — that is what makes it the primary.
func newFleetUnit(t *testing.T, id, name, role string, withRegistry bool) *fleetUnit {
	t.Helper()
	u := &fleetUnit{id: id, eng: engine.New(ringbuf.New(time.Minute, 1<<20))}
	if withRegistry {
		u.reg = peers.NewRegistry(id, 30*time.Second, synth.NewFakeClock(time.Now()))
	}

	st := defaultStatus()
	st.UnitID, st.Name, st.Role, st.FleetSize = id, name, role, 2

	deps := httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Report the live delay so the assertions read the engine, not a
		// canned value.
		Status: statusFunc(func() httpapi.Status {
			s := st
			s.DelayS = u.eng.Delay().Seconds()
			return s
		}),
		Delay:  u.eng,
		Clip:   newFakeClip(10*time.Second, "fake-mp4"),
		Config: &fakeStore{r: config.Default().Runtime()},
	}
	if u.reg != nil {
		deps.Peers = u.reg
	}
	u.srv = httptest.NewServer(httpapi.New(deps))
	t.Cleanup(u.srv.Close)
	return u
}

type statusFunc func() httpapi.Status

func (f statusFunc) Status() httpapi.Status { return f() }

// port extracts a server's port so a member can announce it — the primary
// pairs it with the address the connection came from.
func port(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// IT-10: the requirement in one test. Two appliances on one network, each
// with its own delay: setting one must not disturb the other, and the
// primary's peer list must hand out an address that actually works — that
// address is what the combined page puts behind the second unit's slider.
func TestTwoUnitsKeepIndependentDelays(t *testing.T) {
	primary := newFleetUnit(t, "a1", "Barre", "primary", true)
	member := newFleetUnit(t, "b2", "Window", "member", false)

	// The member announces itself, carrying no address of its own.
	ann := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Window", Port: port(t, member.srv.URL)},
		Gateway: func() (string, error) { return primary.srv.URL, nil },
		Client:  http.DefaultClient,
	}
	resp, err := ann.Once(context.Background())
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if want := []string{"a1", "b2"}; !equalStrings(resp.Roster, want) {
		t.Fatalf("roster = %v, want %v", resp.Roster, want)
	}

	// The primary now lists the member at an address derived from the
	// connection, not from anything the member was configured with.
	listResp, listBody := (&apiClient{t: t, base: primary.srv.URL}).get("/api/v1/peers")
	if listResp.StatusCode != 200 {
		t.Fatalf("GET peers = %d", listResp.StatusCode)
	}
	var list struct {
		Peers []peers.Peer `json:"peers"`
	}
	if err := json.Unmarshal([]byte(listBody), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Peers) != 1 || list.Peers[0].ID != "b2" {
		t.Fatalf("peer list = %s", listBody)
	}
	memberURL := list.Peers[0].BaseURL

	// Drive each slider independently — the member through the address the
	// primary handed out, which proves that address is usable.
	setDelay(t, primary.srv.URL, 12)
	setDelay(t, memberURL, 3)

	if got := primary.status(t); got.DelayS != 12 {
		t.Errorf("primary delay = %v, want 12", got.DelayS)
	}
	if got := member.status(t); got.DelayS != 3 {
		t.Errorf("member delay = %v, want 3", got.DelayS)
	}

	// Move one again; the other must not budge.
	setDelay(t, memberURL, 25)
	if got := primary.status(t); got.DelayS != 12 {
		t.Errorf("primary delay drifted to %v after the member changed", got.DelayS)
	}
	if got := member.status(t); got.DelayS != 25 {
		t.Errorf("member delay = %v, want 25", got.DelayS)
	}

	// Each unit reports its own identity, which is how the page labels the
	// cards and marks which one is hosting the network.
	if got := primary.status(t); got.UnitID != "a1" || got.Role != "primary" {
		t.Errorf("primary status = %+v", got)
	}
	if got := member.status(t); got.UnitID != "b2" || got.Role != "member" {
		t.Errorf("member status = %+v", got)
	}
}

// IT-10: a member that stops announcing drops out of the list on its own, so
// a switched-off unit stops appearing on the page without anyone telling the
// primary about it.
func TestMemberDropsOutWhenItStopsAnnouncing(t *testing.T) {
	clk := synth.NewFakeClock(time.Now())
	primary := &fleetUnit{id: "a1", eng: engine.New(ringbuf.New(time.Minute, 1<<20))}
	primary.reg = peers.NewRegistry("a1", 30*time.Second, clk)
	primary.srv = httptest.NewServer(httpapi.New(httpapi.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Status: &fakeStatus{st: defaultStatus()},
		Delay:  primary.eng,
		Clip:   newFakeClip(10*time.Second, "fake-mp4"),
		Config: &fakeStore{r: config.Default().Runtime()},
		Peers:  primary.reg,
	}))
	t.Cleanup(primary.srv.Close)

	ann := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Window", Port: 8080},
		Gateway: func() (string, error) { return primary.srv.URL, nil },
		Client:  http.DefaultClient,
	}
	if _, err := ann.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if primary.reg.Count() != 1 {
		t.Fatal("member did not register")
	}

	clk.Advance(31 * time.Second)
	_, body := (&apiClient{t: t, base: primary.srv.URL}).get("/api/v1/peers")
	if !strings.Contains(body, `"peers":[]`) {
		t.Fatalf("stale member still listed: %s", body)
	}
}
