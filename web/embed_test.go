package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// UT-50 — the handler behind GET / (httpapi mounts it for every path no API
// route claims) serves the guest quick-download page at the short path the
// poster's QR code carries (FR-19). A QR encodes a URL once and is printed;
// "/clip" is that URL's path, so it has to keep working, and the control
// page on "/" must not be displaced by it.
func TestHandlerServesTheGuestDownloadPage(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	get := func(t *testing.T, path string) (*http.Response, string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: read body: %v", path, err)
		}
		return res, string(body)
	}

	t.Run("clip path serves the download page", func(t *testing.T) {
		res, body := get(t, "/clip")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /clip = %d, want 200", res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET /clip Content-Type = %q, want text/html", ct)
		}
		if !strings.Contains(body, `id="download-again"`) {
			t.Fatalf("GET /clip body does not carry the download-again button:\n%s", body)
		}
	})

	t.Run("root still serves the control page", func(t *testing.T) {
		res, body := get(t, "/")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", res.StatusCode)
		}
		if !strings.Contains(body, `id="units"`) {
			t.Fatal("GET / no longer serves the control page")
		}
	})

	t.Run("stylesheet still served", func(t *testing.T) {
		res, _ := get(t, "/style.css")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET /style.css = %d, want 200", res.StatusCode)
		}
	})
}
