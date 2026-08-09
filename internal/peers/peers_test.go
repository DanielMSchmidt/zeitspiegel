package peers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/peers"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

const ttl = 30 * time.Second

func newRegistry(t *testing.T) (*peers.Registry, *synth.FakeClock) {
	t.Helper()
	clk := synth.NewFakeClock(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	return peers.NewRegistry("a1", ttl, clk), clk
}

func peer(id, name string) peers.Peer {
	return peers.Peer{ID: id, Name: name, BaseURL: "http://10.42.0.7"}
}

// UT-22: a registered peer shows up, and the list is ordered so the UI does
// not reshuffle its cards between polls.
func TestRegisterAndList(t *testing.T) {
	r, _ := newRegistry(t)
	for _, id := range []string{"c3", "b2"} {
		if err := r.Register(peer(id, "Unit "+id)); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}

	got := r.List()
	if len(got) != 2 {
		t.Fatalf("List() = %d peers, want 2", len(got))
	}
	if got[0].ID != "b2" || got[1].ID != "c3" {
		t.Fatalf("List() not sorted by id: %v, %v", got[0].ID, got[1].ID)
	}
	if r.Count() != 2 {
		t.Fatalf("Count() = %d, want 2", r.Count())
	}
}

// UT-22: re-registering replaces rather than duplicates — a member
// heartbeats every few seconds, and its address changes when DHCP moves it.
func TestReRegisterReplaces(t *testing.T) {
	r, clk := newRegistry(t)
	if err := r.Register(peers.Peer{ID: "b2", Name: "Barre", BaseURL: "http://10.42.0.7"}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	if err := r.Register(peers.Peer{ID: "b2", Name: "Barre", BaseURL: "http://10.42.0.9"}); err != nil {
		t.Fatal(err)
	}

	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() = %d peers, want 1", len(got))
	}
	if got[0].BaseURL != "http://10.42.0.9" {
		t.Fatalf("BaseURL = %q, want the newer address", got[0].BaseURL)
	}
	if got[0].AgeS != 0 {
		t.Fatalf("AgeS = %v, want 0 — re-registering refreshes the entry", got[0].AgeS)
	}
}

// UT-22: a unit that goes away stops being listed, so the UI can drop its
// card and the primary's fleet count reflects reality.
func TestEntriesExpireAfterTTL(t *testing.T) {
	r, clk := newRegistry(t)
	if err := r.Register(peer("b2", "Barre")); err != nil {
		t.Fatal(err)
	}

	clk.Advance(ttl - time.Second)
	if got := r.List(); len(got) != 1 {
		t.Fatalf("just inside the TTL: %d peers, want 1", len(got))
	} else if got[0].AgeS != (ttl - time.Second).Seconds() {
		t.Fatalf("AgeS = %v, want %v", got[0].AgeS, (ttl - time.Second).Seconds())
	}

	clk.Advance(2 * time.Second)
	if got := r.List(); len(got) != 0 {
		t.Fatalf("past the TTL: %d peers, want 0", len(got))
	}
	if r.Count() != 0 {
		t.Fatalf("Count() = %d, want 0", r.Count())
	}
}

// UT-22: a heartbeat keeps an entry alive indefinitely.
func TestHeartbeatKeepsEntryAlive(t *testing.T) {
	r, clk := newRegistry(t)
	for i := 0; i < 20; i++ {
		if err := r.Register(peer("b2", "Barre")); err != nil {
			t.Fatal(err)
		}
		clk.Advance(ttl / 3)
	}
	if r.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", r.Count())
	}
}

// UT-22: a unit must never register with itself. This is the guard that
// stops a demoted primary — which becomes 10.42.0.1 again the moment it
// comes back up — from announcing to its own gateway and appearing twice.
func TestSelfRegistrationRejected(t *testing.T) {
	r, _ := newRegistry(t)
	err := r.Register(peer("a1", "Myself"))
	if !errors.Is(err, peers.ErrInvalid) {
		t.Fatalf("Register(self) = %v, want ErrInvalid", err)
	}
	if r.Count() != 0 {
		t.Fatal("self registered anyway")
	}
}

// UT-22: bad input is rejected with the sentinel the API layer maps to 422.
func TestRegisterValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    peers.Peer
	}{
		{"empty id", peers.Peer{ID: "", Name: "x", BaseURL: "http://10.42.0.7"}},
		{"id not a slug", peers.Peer{ID: "Not A Slug", Name: "x", BaseURL: "http://10.42.0.7"}},
		{"empty name", peers.Peer{ID: "b2", Name: "", BaseURL: "http://10.42.0.7"}},
		{"blank name", peers.Peer{ID: "b2", Name: "   ", BaseURL: "http://10.42.0.7"}},
		{"empty url", peers.Peer{ID: "b2", Name: "x", BaseURL: ""}},
		{"non-http url", peers.Peer{ID: "b2", Name: "x", BaseURL: "ftp://10.42.0.7"}},
		{"garbage url", peers.Peer{ID: "b2", Name: "x", BaseURL: "://"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRegistry(t)
			if err := r.Register(tc.p); !errors.Is(err, peers.ErrInvalid) {
				t.Fatalf("Register = %v, want ErrInvalid", err)
			}
		})
	}
}

// UT-22: an over-long name is trimmed rather than refused — the name comes
// off a file a human typed into, and refusing it would silently drop the
// unit out of the fleet.
func TestLongNameIsTruncatedNotRejected(t *testing.T) {
	r, _ := newRegistry(t)
	long := strings.Repeat("x", 200)
	if err := r.Register(peers.Peer{ID: "b2", Name: long, BaseURL: "http://10.42.0.7"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got := r.List()
	if len(got) != 1 {
		t.Fatalf("List() = %d, want 1", len(got))
	}
	if len(got[0].Name) >= len(long) {
		t.Fatalf("name not truncated: %d chars", len(got[0].Name))
	}
}

// UT-22: the roster is what drives promotion order, so it must contain this
// unit as well as its peers, sorted, and must drop the dead ones.
func TestRosterIncludesSelfAndIsSorted(t *testing.T) {
	r, clk := newRegistry(t)
	for _, id := range []string{"c3", "b2"} {
		if err := r.Register(peer(id, "Unit")); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := r.Roster(), []string{"a1", "b2", "c3"}; !equal(got, want) {
		t.Fatalf("Roster() = %v, want %v", got, want)
	}

	clk.Advance(ttl + time.Second)
	if got, want := r.Roster(), []string{"a1"}; !equal(got, want) {
		t.Fatalf("Roster() after expiry = %v, want %v", got, want)
	}
}

// UT-22: Position is what a registering member is told, so it knows how long
// to wait before promoting if the primary disappears.
func TestPosition(t *testing.T) {
	r, _ := newRegistry(t)
	for _, id := range []string{"c3", "b2"} {
		if err := r.Register(peer(id, "Unit")); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]int{"a1": 0, "b2": 1, "c3": 2} {
		if got := r.Position(id); got != want {
			t.Errorf("Position(%q) = %d, want %d", id, got, want)
		}
	}
	if got := r.Position("zz"); got != -1 {
		t.Errorf("Position of an unknown id = %d, want -1", got)
	}
}

// UT-22: the registry is read by the HTTP handlers and written by the
// announce handler; -race would catch a missing lock here.
func TestRegistryIsConcurrencySafe(t *testing.T) {
	r, _ := newRegistry(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			_ = r.Register(peer("b2", "Barre"))
		}
	}()
	for i := 0; i < 500; i++ {
		r.List()
		r.Roster()
		r.Count()
	}
	<-done
}

// --- announcer ---

type fakeDoer struct {
	calls int
	fn    func(*http.Request) (*http.Response, error)
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.calls++
	return f.fn(r)
}

func jsonResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// manualTicker matches the injected-ticker shape httpapi already uses, so
// the announcer can be driven a tick at a time with no wall clock.
func manualTicker(ch chan time.Time) func(time.Duration) (<-chan time.Time, func()) {
	return func(time.Duration) (<-chan time.Time, func()) { return ch, func() {} }
}

// UT-25: the announcer posts to whatever the gateway is — the member never
// has to know its own address or the primary's, which is what makes the
// image identical on every card.
func TestAnnouncerPostsToGateway(t *testing.T) {
	var gotURL, gotBody string
	doer := &fakeDoer{fn: func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return jsonResponse(200, `{"id":"b2","base_url":"http://10.42.0.7:80","roster":["a1","b2"],"position":1}`), nil
	}}

	var roster []string
	a := &peers.Announcer{
		Self:     peers.Announcement{ID: "b2", Name: "Barre", Port: 80},
		Gateway:  func() (string, error) { return "http://10.42.0.1", nil },
		Client:   doer,
		OnRoster: func(ids []string) { roster = ids },
	}

	resp, err := a.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if gotURL != "http://10.42.0.1/api/v1/peers" {
		t.Fatalf("posted to %q", gotURL)
	}
	if !strings.Contains(gotBody, `"id":"b2"`) || !strings.Contains(gotBody, `"port":80`) {
		t.Fatalf("body = %s", gotBody)
	}
	if resp.Position != 1 {
		t.Fatalf("Position = %d, want 1", resp.Position)
	}
	if !equal(roster, []string{"a1", "b2"}) {
		t.Fatalf("OnRoster got %v", roster)
	}
}

// UT-25: no gateway yet (the radio is still associating) is an ordinary
// error, not a crash.
func TestAnnouncerWithoutGateway(t *testing.T) {
	a := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Barre", Port: 80},
		Gateway: func() (string, error) { return "", errors.New("no route") },
		Client:  &fakeDoer{fn: func(*http.Request) (*http.Response, error) { return nil, errors.New("unreachable") }},
	}
	if _, err := a.Once(context.Background()); err == nil {
		t.Fatal("expected an error when there is no gateway")
	}
}

// UT-25: a rejected announcement surfaces as an error rather than being
// mistaken for success.
func TestAnnouncerSurfacesHTTPError(t *testing.T) {
	a := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Barre", Port: 80},
		Gateway: func() (string, error) { return "http://10.42.0.1", nil },
		Client: &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			return jsonResponse(422, `{"title":"invalid peer"}`), nil
		}},
	}
	if _, err := a.Once(context.Background()); err == nil {
		t.Fatal("expected an error for a 422 response")
	}
}

// UT-25: the loop announces on every tick and a failure does not kill it —
// a member must keep trying while the primary reboots, forever.
func TestAnnouncerRunRetriesAfterFailure(t *testing.T) {
	tick := make(chan time.Time)
	var errs int
	doer := &fakeDoer{fn: func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}}
	a := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Barre", Port: 80},
		Gateway: func() (string, error) { return "http://10.42.0.1", nil },
		Client:  doer,
		Ticker:  manualTicker(tick),
		OnError: func(error) { errs++ },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	for i := 0; i < 3; i++ {
		tick <- time.Now()
	}
	// A fourth tick only lands once the third has been processed, so by now
	// at least three attempts have been made.
	tick <- time.Now()
	cancel()
	<-done

	if doer.calls < 3 {
		t.Fatalf("announced %d times across 4 ticks, want at least 3", doer.calls)
	}
	if errs < 3 {
		t.Fatalf("reported %d errors, want at least 3 — failures must be visible", errs)
	}
}

// UT-25: Run announces once immediately rather than idling for a full tick,
// so a member appears in the primary's list as soon as it associates.
func TestAnnouncerRunAnnouncesImmediately(t *testing.T) {
	tick := make(chan time.Time)
	announced := make(chan struct{}, 1)
	a := &peers.Announcer{
		Self:    peers.Announcement{ID: "b2", Name: "Barre", Port: 80},
		Gateway: func() (string, error) { return "http://10.42.0.1", nil },
		Client: &fakeDoer{fn: func(*http.Request) (*http.Response, error) {
			select {
			case announced <- struct{}{}:
			default:
			}
			return jsonResponse(200, `{"id":"b2","roster":["a1","b2"],"position":1}`), nil
		}},
		Ticker: manualTicker(tick),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	select {
	case <-announced:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not announce before the first tick")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
