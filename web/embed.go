// Package web embeds the static control UI (ARCHITECTURE §D5: one page, no
// build tooling).
package web

import (
	"embed"
	"net/http"
)

//go:embed index.html settings.html style.css
var content embed.FS

// Handler serves the UI; the file server resolves "/" to index.html.
func Handler() http.Handler {
	return http.FileServer(http.FS(content))
}
