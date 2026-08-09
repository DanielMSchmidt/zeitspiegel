package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/danielmschmidt/zeitspiegel/internal/httpapi"
	"github.com/danielmschmidt/zeitspiegel/internal/peers"
	"github.com/danielmschmidt/zeitspiegel/internal/synth"
)

func withRegistry(t *testing.T) (*apiClient, *peers.Registry) {
	t.Helper()
	reg := peers.NewRegistry("a1", 30*time.Second, synth.NewFakeClock(time.Now()))
	srv := newServer(t, func(d *httpapi.Deps) { d.Peers = reg })
	return &apiClient{t: t, base: srv.URL}, reg
}

// apiClient is a tiny request helper — the existing suite reads responses
// inline, but the peer tests make the same three calls repeatedly.
type apiClient struct {
	t    *testing.T
	base string
}

func (h *apiClient) post(path, body string) (*http.Response, string) {
	h.t.Helper()
	resp, err := http.Post(h.base+path, "application/json", strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	return resp, readAll(h.t, resp)
}

func (h *apiClient) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := http.Get(h.base + path)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp, readAll(h.t, resp)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

// UT-23: a member announces itself with no address at all; the primary fills
// it in from the connection the announcement arrived on. This is what lets
// every card ship the identical image — a member never has to discover its
// own DHCP lease.
func TestRegisterDerivesBaseURLFromRemoteAddr(t *testing.T) {
	h, reg := withRegistry(t)

	resp, body := h.post("/api/v1/peers", `{"id":"b2","name":"Barre","port":8080}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	var out peers.RegisterResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if out.ID != "b2" {
		t.Errorf("id = %q", out.ID)
	}
	if !strings.HasPrefix(out.BaseURL, "http://127.0.0.1:") || !strings.HasSuffix(out.BaseURL, ":8080") {
		t.Errorf("base_url = %q, want the caller's address with the announced port", out.BaseURL)
	}
	if got := reg.List(); len(got) != 1 || got[0].BaseURL != out.BaseURL {
		t.Errorf("registry holds %+v, want the derived address", got)
	}
}

// UT-23: the reply carries the roster and this member's place in it, which
// is exactly what the member needs to know how long to wait before promoting
// if the primary disappears.
func TestRegisterReturnsRosterAndPosition(t *testing.T) {
	h, _ := withRegistry(t)
	h.post("/api/v1/peers", `{"id":"c3","name":"Window","port":80}`)
	_, body := h.post("/api/v1/peers", `{"id":"b2","name":"Barre","port":80}`)

	var out peers.RegisterResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a1", "b2", "c3"}; !equalStrings(out.Roster, want) {
		t.Errorf("roster = %v, want %v", out.Roster, want)
	}
	if out.Position != 1 {
		t.Errorf("position = %d, want 1 (a1 is the primary, b2 is next)", out.Position)
	}
}

// UT-23: the list is what the combined page renders.
func TestListPeers(t *testing.T) {
	h, _ := withRegistry(t)
	h.post("/api/v1/peers", `{"id":"c3","name":"Window","port":80}`)
	h.post("/api/v1/peers", `{"id":"b2","name":"Barre","port":80}`)

	resp, body := h.get("/api/v1/peers")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Peers []peers.Peer `json:"peers"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(out.Peers) != 2 {
		t.Fatalf("got %d peers, want 2: %s", len(out.Peers), body)
	}
	if out.Peers[0].ID != "b2" || out.Peers[1].ID != "c3" {
		t.Errorf("peers not sorted by id: %s", body)
	}
	if out.Peers[0].Name != "Barre" {
		t.Errorf("name = %q, want Barre", out.Peers[0].Name)
	}
}

// UT-23: invalid announcements are 422 problem+json (FR-11), never a silent
// partial registration.
func TestRegisterValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed json", `{`},
		{"missing id", `{"name":"Barre","port":80}`},
		{"id not a slug", `{"id":"Not A Slug","name":"Barre","port":80}`},
		{"blank name", `{"id":"b2","name":"   ","port":80}`},
		{"self id", `{"id":"a1","name":"Myself","port":80}`},
		{"port zero", `{"id":"b2","name":"Barre","port":0}`},
		{"port negative", `{"id":"b2","name":"Barre","port":-1}`},
		{"port too large", `{"id":"b2","name":"Barre","port":70000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, reg := withRegistry(t)
			resp, body := h.post("/api/v1/peers", tc.body)
			if resp.StatusCode != 422 {
				t.Fatalf("status = %d, want 422 (body %s)", resp.StatusCode, body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", ct)
			}
			if reg.Count() != 0 {
				t.Error("an invalid announcement was registered anyway")
			}
		})
	}
}

// UT-23: a unit with no registry wired (a plain single appliance) simply has
// no fleet API, rather than serving an empty one.
func TestPeerRoutesAbsentWithoutRegistry(t *testing.T) {
	srv := newServer(t, nil)
	h := &apiClient{t: t, base: srv.URL}
	if resp, _ := h.get("/api/v1/peers"); resp.StatusCode != 404 {
		t.Errorf("GET /api/v1/peers = %d, want 404 when no registry is wired", resp.StatusCode)
	}
}

// UT-23: status carries the identity and role the combined page labels each
// card with (REQUIREMENTS §3).
func TestStatusCarriesIdentityAndRole(t *testing.T) {
	st := defaultStatus()
	st.UnitID = "b2"
	st.Name = "Barre"
	st.Role = "member"
	st.FleetSize = 3
	srv := newServer(t, func(d *httpapi.Deps) { d.Status = &fakeStatus{st: st} })

	h := &apiClient{t: t, base: srv.URL}
	_, body := h.get("/api/v1/status")
	var got httpapi.Status
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.UnitID != "b2" || got.Name != "Barre" || got.Role != "member" || got.FleetSize != 3 {
		t.Fatalf("status = %+v, want the identity fields preserved", got)
	}
	for _, key := range []string{`"unit_id"`, `"name"`, `"role"`, `"fleet_size"`} {
		if !strings.Contains(body, key) {
			t.Errorf("response is missing %s: %s", key, body)
		}
	}
}

// UT-24: the combined page is served by the primary but talks straight to
// each unit, so every unit has to allow the cross-origin call. The appliance
// is already unauthenticated on an isolated, internet-less LAN (NFR-6), so
// this widens nothing.
func TestCORSHeadersOnAPIResponses(t *testing.T) {
	h, _ := withRegistry(t)
	for _, path := range []string{"/api/v1/status", "/api/v1/peers", "/api/v1/config"} {
		resp, _ := h.get(path)
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want *", path, got)
		}
	}
}

// UT-24: a JSON PUT is preflighted, so the delay slider on a peer's card
// does not work at all without an OPTIONS answer.
func TestPreflightIsAnswered(t *testing.T) {
	h, _ := withRegistry(t)
	req, err := http.NewRequest(http.MethodOptions, h.base+"/api/v1/delay", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://10.42.0.1")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "content-type")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") {
		t.Errorf("Allow-Methods = %q, want it to include PUT", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "content-type") {
		t.Errorf("Allow-Headers = %q, want it to include Content-Type", got)
	}
}

// UT-24: the delay PUT itself must still work normally with CORS in place —
// the wrapper must not swallow real requests.
func TestDelayStillWorksWithCORS(t *testing.T) {
	h, _ := withRegistry(t)
	req, err := http.NewRequest(http.MethodPut, h.base+"/api/v1/delay", strings.NewReader(`{"seconds":4.5}`))
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
		t.Fatalf("PUT /api/v1/delay = %d, want 200", resp.StatusCode)
	}
}

func equalStrings(a, b []string) bool {
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
