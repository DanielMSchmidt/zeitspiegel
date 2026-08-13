package main

import (
	"runtime/debug"
	"strings"
)

// version is stamped at link time by the Makefile:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always --dirty)"
//
// It is deliberately not a constant: every card ships the byte-identical
// image (E-8), so the build a unit is running is not something the card can be
// asked about — only the binary knows, and only if the build told it.
var version = ""

// buildVersion is what the unit calls itself in its logs, at /debug/vars, and
// on the boot partition where a pulled card can be read without booting it.
// An unstamped build says so instead of returning an empty string: a blank
// version field in a bug report looks like a collection bug, and a made-up one
// is worse than an admitted unknown.
func buildVersion() string {
	if v := strings.TrimSpace(version); v != "" {
		return v
	}
	// A plain `go build` embeds the VCS state by itself, which covers
	// development binaries. The Pi cross-build turns that off (the Docker
	// mount is not a usable git checkout), which is why the Makefile stamps
	// -X for the builds that end up on a card.
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			return rev + dirty
		}
	}
	return "unstamped"
}
