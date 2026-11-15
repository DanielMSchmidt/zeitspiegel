// Package web embeds the static control UI (ARCHITECTURE §D5: one page, no
// build tooling).
package web

import (
	"embed"
	"net/http"
)

//go:embed index.html settings.html clip.html style.css
var content embed.FS

// Handler serves the UI; the file server resolves "/" to index.html. The
// guest quick-download page also answers on the short path "/clip" (FR-19):
// that is the URL printed inside the poster's QR code, and a printed QR
// cannot be re-issued when a path moves.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/clip", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, content, "clip.html")
	})
	mux.Handle("/", http.FileServer(http.FS(content)))
	return mux
}
