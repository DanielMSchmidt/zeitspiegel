// Package identity derives a unit's stable id and display name from the
// hardware and from a file on the boot partition, so that every appliance can
// ship the identical SD image and still tell itself apart from its neighbours
// (E-8).
//
// The id is the registry key, the mDNS hostname component and the election
// tiebreak, so it must be stable across reboots. The display name is whatever
// a human wrote on the card; it is cosmetic.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
)

const (
	// IDLen is how many hex digits of the hardware identifier make up the
	// unit id. Six is plenty to separate a handful of units in one room and
	// short enough to read off a screen.
	IDLen = 6
	// MaxNameLen bounds the display name; longer names are truncated rather
	// than rejected, because the file is edited by hand.
	MaxNameLen = 32
	// DefaultCPUInfoPath is where the Pi publishes its serial.
	DefaultCPUInfoPath = "/proc/cpuinfo"
	// DefaultMACPath is the fallback hardware identifier.
	DefaultMACPath = "/sys/class/net/wlan0/address"
	// DefaultNameFile lives on the FAT32 boot partition, which stays
	// writable when the root overlay is sealed — so a unit can be named by
	// plugging its card into any computer, with no image rebuild.
	DefaultNameFile = "/boot/firmware/zeitspiegel-name.txt"
)

// Unit is an appliance's identity on the network.
type Unit struct {
	ID   string
	Name string
}

// Sources locates what identity is derived from. Zero-valued paths fall back
// to the Default* constants; tests point them at fixtures.
type Sources struct {
	CPUInfoPath string
	MACPath     string
	NameFile    string
	// OverrideID and Override force the id and name. They exist for
	// development and for the E2E lane, where several units share one
	// machine and therefore one /proc/cpuinfo.
	OverrideID string
	Override   string
}

// Resolve determines the unit's identity. The bool reports whether the id is
// stable across reboots; false means both hardware sources failed and a
// random id was minted, which the caller should log loudly — an id that
// changes every boot makes the peer registry and the election unreliable.
func Resolve(s Sources) (Unit, bool) {
	id, stable := resolveID(s)
	return Unit{ID: id, Name: resolveName(s, id)}, stable
}

func resolveID(s Sources) (string, bool) {
	if ValidID(s.OverrideID) {
		return s.OverrideID, true
	}
	if id, ok := parseSerial(readFile(orDefault(s.CPUInfoPath, DefaultCPUInfoPath))); ok {
		return id, true
	}
	if id, ok := parseMAC(readFile(orDefault(s.MACPath, DefaultMACPath))); ok {
		return id, true
	}
	return randomID(), false
}

func resolveName(s Sources, id string) string {
	if n := cleanName(s.Override); n != "" {
		return n
	}
	if n := cleanName(readFile(orDefault(s.NameFile, DefaultNameFile))); n != "" {
		return n
	}
	return DefaultName(id)
}

// DefaultName is what a card that was never named calls itself.
func DefaultName(id string) string { return "Zeitspiegel " + strings.ToUpper(id) }

// ValidID reports whether id is usable as a registry key and as an mDNS
// hostname component: lowercase alphanumerics and hyphens, 1…32 characters.
func ValidID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseSerial pulls the "Serial : ..." line out of /proc/cpuinfo and keeps
// its last IDLen hex digits. Pi serials share a long constant prefix, so the
// tail is what actually distinguishes two boards.
func parseSerial(cpuinfo string) (string, bool) {
	for _, line := range strings.Split(cpuinfo, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "serial") {
			continue
		}
		return tailHex(strings.TrimSpace(value))
	}
	return "", false
}

// parseMAC keeps the last IDLen hex digits of a MAC address — the vendor
// prefix is shared, the tail is not.
func parseMAC(mac string) (string, bool) {
	return tailHex(strings.ReplaceAll(strings.TrimSpace(mac), ":", ""))
}

// tailHex normalises a hardware identifier to the last IDLen lowercase hex
// digits, rejecting placeholders like all-zero serials.
func tailHex(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	var hexOnly strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			hexOnly.WriteRune(r)
		}
	}
	s = hexOnly.String()
	if len(s) < IDLen || strings.Trim(s, "0") == "" {
		return "", false
	}
	return s[len(s)-IDLen:], true
}

func randomID() string {
	b := make([]byte, (IDLen+1)/2)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; a fixed id is still a
		// working (if colliding) fallback and beats refusing to boot.
		return strings.Repeat("f", IDLen)
	}
	return hex.EncodeToString(b)[:IDLen]
}

// cleanName reduces raw file contents to a display name: the first
// non-blank line, trimmed and truncated. Empty means "no name given".
func cleanName(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		name := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if name == "" {
			continue
		}
		if r := []rune(name); len(r) > MaxNameLen {
			name = string(r[:MaxNameLen])
		}
		return name
	}
	return ""
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
