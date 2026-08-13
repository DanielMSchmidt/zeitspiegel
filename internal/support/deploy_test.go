package support

import (
	"os"
	"strings"
	"testing"
)

func readDeploy(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(repoPath(t, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// UT-36: without SDL_VIDEODRIVER the library probes wayland and x11 first on a
// machine that has neither, which buried the real failure under an
// XDG_RUNTIME_DIR complaint. The appliance has exactly one video path — KMSDRM
// on the DRM node — so it says so, and points XDG_RUNTIME_DIR at the tmpfs the
// unit already gets from RuntimeDirectory.
func TestServiceUnitPinsTheVideoDriver(t *testing.T) {
	unit := readDeploy(t, "deploy/zeitspiegel.service")
	for _, want := range []string{
		"Environment=SDL_VIDEODRIVER=kmsdrm",
		"Environment=XDG_RUNTIME_DIR=/run/zeitspiegel",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit does not set %q:\n%s", want, unit)
		}
	}
	// The directory that variable points at has to be the one systemd makes.
	if !strings.Contains(unit, "RuntimeDirectory=zeitspiegel") {
		t.Error("XDG_RUNTIME_DIR points at a directory the unit does not create")
	}
}

// UT-36: frame dumping and debug logging belong to a bench card. A venue unit
// is sealed, so the frames would land in tmpfs and vanish anyway — but the
// reason to keep them off it is not that they are useless there, it is that
// nobody should have to wonder whether a production card is writing pictures
// of the room to storage every five seconds.
//
// The guarantee is structural: the shipped config leaves the key empty, and
// the bake only writes a value inside its development branch. This pins that,
// because the next person to edit a package list will not be thinking about
// it.
func TestFrameDumpingIsDevelopmentOnly(t *testing.T) {
	cfg := readDeploy(t, "deploy/config.toml")
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // documented, not enabled
		}
		if strings.HasPrefix(trimmed, "frame_dump_dir") || strings.HasPrefix(trimmed, "log_level") {
			t.Errorf("the shipped config enables a development aid: %q", trimmed)
		}
	}

	bake := readDeploy(t, "deploy/sd/bake.sh")
	dev := devBranch(t, bake)
	// The TOML assignment form, not the bare word: bake.sh also passes
	// udev.log_level=3 on the kernel cmdline, which is a different thing
	// entirely and belongs on every card.
	for _, key := range []string{"frame_dump_dir = ", "log_level = "} {
		if !strings.Contains(dev, key) {
			t.Errorf("the development bake does not set %s; `make sd-dev` would produce a card that cannot be debugged", key)
		}
		if strings.Count(bake, key) != strings.Count(dev, key) {
			t.Errorf("%s is set outside the development branch — a production card would carry it", key)
		}
	}
}

// devBranch returns the body of bake.sh's `if [[ "$SEAL" != 1 ]]` block: what
// a development image gets and a production one does not.
func devBranch(t *testing.T, bake string) string {
	t.Helper()
	const open = `if [[ "$SEAL" != 1 ]]; then`
	i := strings.Index(bake, open)
	if i < 0 {
		t.Fatal("bake.sh has no development branch; the split between card kinds is gone")
	}
	rest := bake[i+len(open):]
	j := strings.Index(rest, "\nfi\n")
	if j < 0 {
		t.Fatal("bake.sh's development branch is not closed")
	}
	return rest[:j]
}
