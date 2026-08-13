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
