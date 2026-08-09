// Package peers is the fleet's membership list. Whichever unit is currently
// hosting the access point keeps a Registry of the members that have
// announced themselves to it; every other unit runs an Announcer that
// heartbeats to its default gateway — which, by construction, is the primary
// (E-8).
//
// That indirection is the point: a member never has to be told its own
// address or the primary's, so every card can ship the identical image. The
// primary fills in each member's address from the connection it arrived on.
//
// The Registry is pure: injected clock (hard rule 6), no logging, errors
// returned as values. Its state is deliberately in-RAM only — the root
// filesystem is read-only, and a rebooted primary should start from an empty
// list and let the members re-announce.
package peers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/identity"
)

// ErrInvalid marks a rejected announcement. The API layer maps it to 422.
var ErrInvalid = errors.New("invalid peer")

// Clock abstracts time so the registry stays wall-clock-free; tests use
// synth.FakeClock.
type Clock interface {
	Now() time.Time
}

// Peer is one unit as seen by the primary.
type Peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// AgeS is how long ago this peer last announced itself. The UI uses it
	// to show a card as stale before the TTL drops it entirely.
	AgeS float64 `json:"age_s"`
}

type entry struct {
	peer Peer
	seen time.Time
}

// Registry is the primary's live membership list, keyed by unit id.
type Registry struct {
	mu     sync.Mutex
	selfID string
	ttl    time.Duration
	clk    Clock
	m      map[string]entry
}

// NewRegistry returns a registry owned by the unit with id selfID. Entries
// that have not been refreshed within ttl are dropped.
func NewRegistry(selfID string, ttl time.Duration, clk Clock) *Registry {
	return &Registry{selfID: selfID, ttl: ttl, clk: clk, m: map[string]entry{}}
}

// Register records or refreshes a peer. Re-registering replaces the entry,
// so a member that moved to a new DHCP address simply announces again.
func (r *Registry) Register(p Peer) error {
	if !identity.ValidID(p.ID) {
		return fmt.Errorf("id %q: %w (lowercase letters, digits and hyphens, 1-32 chars)", p.ID, ErrInvalid)
	}
	if p.ID == r.selfID {
		// A demoted primary becomes 10.42.0.1 again when it comes back up.
		// Without this guard it would announce to itself and appear twice.
		return fmt.Errorf("id %q is this unit: %w", p.ID, ErrInvalid)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("name: %w (must not be blank)", ErrInvalid)
	}
	if runes := []rune(name); len(runes) > identity.MaxNameLen {
		// Truncate rather than refuse: the name comes off a file a human
		// typed into, and refusing it would drop the unit out of the fleet.
		name = string(runes[:identity.MaxNameLen])
	}
	if err := validBaseURL(p.BaseURL); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[p.ID] = entry{
		peer: Peer{ID: p.ID, Name: name, BaseURL: strings.TrimSuffix(p.BaseURL, "/")},
		seen: r.clk.Now(),
	}
	return nil
}

func validBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("base_url: %w (must not be empty)", ErrInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url %q: %w (%v)", raw, ErrInvalid, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url %q: %w (must be http or https)", raw, ErrInvalid)
	}
	if u.Host == "" {
		return fmt.Errorf("base_url %q: %w (no host)", raw, ErrInvalid)
	}
	return nil
}

// List returns the live peers sorted by id, each stamped with how long ago
// it was last seen. Sorting keeps the UI's cards from reshuffling between
// polls.
func (r *Registry) List() []Peer {
	now, live := r.snapshot()
	out := make([]Peer, 0, len(live))
	for _, e := range live {
		p := e.peer
		p.AgeS = now.Sub(e.seen).Seconds()
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Count is the number of live peers, which is what the election watches to
// decide whether it might be half of a split brain.
func (r *Registry) Count() int {
	_, live := r.snapshot()
	return len(live)
}

// Roster is every live unit id including this one, sorted. It is what gives
// promotion a deterministic order after the primary disappears.
func (r *Registry) Roster() []string {
	_, live := r.snapshot()
	ids := make([]string, 0, len(live)+1)
	ids = append(ids, r.selfID)
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Position is a unit's index in the roster, or -1 if it is not in it.
func (r *Registry) Position(id string) int {
	roster := r.Roster()
	for i, got := range roster {
		if got == id {
			return i
		}
	}
	return -1
}

// snapshot evicts expired entries and returns the survivors. Eviction on
// read keeps the registry free of a background goroutine.
func (r *Registry) snapshot() (time.Time, map[string]entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clk.Now()
	for id, e := range r.m {
		if now.Sub(e.seen) > r.ttl {
			delete(r.m, id)
		}
	}
	live := make(map[string]entry, len(r.m))
	for id, e := range r.m {
		live[id] = e
	}
	return now, live
}

// --- announcer ---

// Announcement is what a member tells the primary about itself. It carries no
// address: the primary fills that in from the connection, so a member never
// needs to discover its own DHCP lease.
type Announcement struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

// RegisterResponse is the primary's reply. The roster and position are what
// the member feeds back into its election machine.
type RegisterResponse struct {
	ID       string   `json:"id"`
	BaseURL  string   `json:"base_url"`
	Roster   []string `json:"roster"`
	Position int      `json:"position"`
}

// Doer is the subset of *http.Client the announcer needs.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// DefaultInterval is how often a member heartbeats. It must be comfortably
// below the registry TTL so a single lost packet never drops a unit out of
// the list.
const DefaultInterval = 10 * time.Second

// Announcer heartbeats this unit's existence to the current primary.
//
// It reports failures through OnError rather than logging, so the package
// keeps no logger of its own; cmd decides what to do with them. Failures are
// never fatal — a member must keep trying for as long as the primary is
// rebooting, which is to say indefinitely.
type Announcer struct {
	Self Announcement
	// Gateway returns the base URL of the current primary — in production
	// the default route, which is the AP by construction.
	Gateway func() (string, error)
	Client  Doer
	// Ticker is injected so the loop stays wall-clock-free (hard rule 6);
	// cmd passes a real time.Ticker. Same shape as httpapi.Deps.Ticker.
	Ticker   func(time.Duration) (<-chan time.Time, func())
	Interval time.Duration
	// OnRoster receives the fleet roster from each successful announcement.
	OnRoster func([]string)
	// OnError receives every failed attempt.
	OnError func(error)
}

// Once makes a single announcement.
func (a *Announcer) Once(ctx context.Context) (RegisterResponse, error) {
	gw, err := a.Gateway()
	if err != nil {
		return RegisterResponse{}, fmt.Errorf("gateway: %w", err)
	}
	body, err := json.Marshal(a.Self)
	if err != nil {
		return RegisterResponse{}, fmt.Errorf("encode announcement: %w", err)
	}

	endpoint := strings.TrimSuffix(gw, "/") + "/api/v1/peers"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return RegisterResponse{}, fmt.Errorf("announce request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.Client.Do(req)
	if err != nil {
		return RegisterResponse{}, fmt.Errorf("announce to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	// Bound the reply: it is a short JSON object, and the primary is not
	// authenticated in v1 (NFR-6).
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return RegisterResponse{}, fmt.Errorf("read announce reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return RegisterResponse{}, fmt.Errorf("announce to %s: status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var out RegisterResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return RegisterResponse{}, fmt.Errorf("decode announce reply: %w", err)
	}
	if a.OnRoster != nil && len(out.Roster) > 0 {
		a.OnRoster(out.Roster)
	}
	return out, nil
}

// Run announces immediately and then on every tick until ctx is done. It
// returns only when the context is cancelled: no announcement failure ends
// the loop.
func (a *Announcer) Run(ctx context.Context) error {
	interval := a.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	newTicker := a.Ticker
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	tick, stop := newTicker(interval)
	defer stop()

	a.attempt(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			a.attempt(ctx)
		}
	}
}

func (a *Announcer) attempt(ctx context.Context) {
	if _, err := a.Once(ctx); err != nil && a.OnError != nil {
		a.OnError(err)
	}
}
