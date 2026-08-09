package identity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/identity"
)

const piCPUInfo = `processor	: 0
BogoMIPS	: 108.00
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x4
CPU part	: 0xd0b
CPU revision	: 1

Revision	: d04170
Serial		: 100000003d1f9a2b
Model		: Raspberry Pi 5 Model B Rev 1.0
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// UT-21: the unit id comes from the Pi's CPU serial, so it is stable across
// reboots and re-flashes without any per-card configuration.
func TestIDFromCPUSerial(t *testing.T) {
	dir := t.TempDir()
	u, stable := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
	})
	if !stable {
		t.Fatal("id from a real CPU serial should be reported stable")
	}
	if u.ID != "3d1f9a2b"[len("3d1f9a2b")-identity.IDLen:] {
		t.Fatalf("ID = %q, want the last %d hex digits of the serial", u.ID, identity.IDLen)
	}
	if !identity.ValidID(u.ID) {
		t.Fatalf("ID %q is not a valid slug", u.ID)
	}
}

// UT-21: the same serial must always give the same id — it is the registry
// key, the mDNS hostname and the election tiebreak.
func TestIDIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "cpuinfo", piCPUInfo)
	first, _ := identity.Resolve(identity.Sources{CPUInfoPath: p})
	second, _ := identity.Resolve(identity.Sources{CPUInfoPath: p})
	if first.ID != second.ID {
		t.Fatalf("id not deterministic: %q vs %q", first.ID, second.ID)
	}
}

// UT-21: no CPU serial (a dev box, or a board that does not publish one)
// falls back to the wlan0 MAC, which is still stable per machine.
func TestIDFallsBackToMAC(t *testing.T) {
	dir := t.TempDir()
	u, stable := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", "processor\t: 0\n"),
		MACPath:     writeFile(t, dir, "address", "2c:cf:67:1a:b3:4e\n"),
	})
	if !stable {
		t.Fatal("id from a MAC should still be reported stable")
	}
	if !identity.ValidID(u.ID) {
		t.Fatalf("ID %q is not a valid slug", u.ID)
	}
	if strings.Contains(u.ID, ":") {
		t.Fatalf("ID %q still contains MAC punctuation", u.ID)
	}
}

// UT-21: with neither source the unit still gets an id, but reports itself
// unstable so the caller can log it loudly — an id that changes every boot
// makes the election tiebreak and the peer registry unreliable.
func TestIDFallsBackToRandomAndReportsUnstable(t *testing.T) {
	u, stable := identity.Resolve(identity.Sources{
		CPUInfoPath: "/nonexistent/cpuinfo",
		MACPath:     "/nonexistent/address",
	})
	if stable {
		t.Fatal("a random id must not be reported as stable")
	}
	if !identity.ValidID(u.ID) {
		t.Fatalf("ID %q is not a valid slug", u.ID)
	}
}

// UT-21: with no name file the unit names itself after its id, so a card
// that was never named still shows something meaningful in the UI.
func TestNameDefaultsToID(t *testing.T) {
	dir := t.TempDir()
	u, _ := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
		NameFile:    filepath.Join(dir, "absent.txt"),
	})
	if u.Name != identity.DefaultName(u.ID) {
		t.Fatalf("Name = %q, want %q", u.Name, identity.DefaultName(u.ID))
	}
	if !strings.Contains(u.Name, "Zeitspiegel") {
		t.Fatalf("default name %q should be recognisable", u.Name)
	}
}

// UT-21: the name file is what a human edits by plugging the card into a
// laptop, so it has to tolerate the mess that produces — trailing newlines,
// stray whitespace, a second line, an editor's blank file.
func TestNameFileParsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string // "" ⇒ fall back to the default name
	}{
		{"plain", "Barre", "Barre"},
		{"trailing newline", "Barre\n", "Barre"},
		{"crlf", "Barre\r\n", "Barre"},
		{"surrounding space", "  Window seat  \n", "Window seat"},
		{"only the first line", "Barre\nignored second line\n", "Barre"},
		{"empty", "", ""},
		{"whitespace only", "   \n\t\n", ""},
		{"leading blank line", "\nBarre\n", "Barre"},
		{"over-length truncated", strings.Repeat("x", identity.MaxNameLen+20), strings.Repeat("x", identity.MaxNameLen)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			u, _ := identity.Resolve(identity.Sources{
				CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
				NameFile:    writeFile(t, dir, "name.txt", tc.raw),
			})
			want := tc.want
			if want == "" {
				want = identity.DefaultName(u.ID)
			}
			if u.Name != want {
				t.Fatalf("Name = %q, want %q", u.Name, want)
			}
		})
	}
}

// UT-21: ids are used verbatim as an mDNS hostname component, so the slug
// rule has to actually reject what would break one.
func TestValidID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"a3f2", true},
		{"3d1f9a2b", true},
		{"unit-2", true},
		{"", false},
		{"UPPER", false},
		{"has space", false},
		{"dot.dot", false},
		{"under_score", false},
		{strings.Repeat("a", 32), true},
		{strings.Repeat("a", 33), false},
	} {
		if got := identity.ValidID(tc.id); got != tc.want {
			t.Errorf("ValidID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// UT-21: an explicit override wins over the hardware — this is what lets the
// E2E lane run three units on one machine, where /proc/cpuinfo is identical.
func TestOverrideWins(t *testing.T) {
	dir := t.TempDir()
	u, stable := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
		NameFile:    writeFile(t, dir, "name.txt", "From File"),
		OverrideID:  "unit-2",
		Override:    "Station B",
	})
	if !stable {
		t.Fatal("an explicit id is stable by definition")
	}
	if u.ID != "unit-2" || u.Name != "Station B" {
		t.Fatalf("got %+v, want id unit-2 / name Station B", u)
	}
}

// UT-21: an invalid override must not silently become the hostname.
func TestInvalidOverrideIDIsRejected(t *testing.T) {
	dir := t.TempDir()
	u, _ := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
		OverrideID:  "Not A Slug",
	})
	if u.ID == "Not A Slug" {
		t.Fatal("invalid override id was accepted")
	}
	if !identity.ValidID(u.ID) {
		t.Fatalf("ID = %q, want a fallback to the hardware id", u.ID)
	}
}
