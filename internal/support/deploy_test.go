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

// UT-36: SDL's KMSDRM backend dlopens libEGL.so.1 at runtime, so it is not a
// dependency of libsdl2 and nothing fails at build time when it is absent —
// the image builds, boots, and the binary dies with "EGL not initialized" on a
// screen nobody is watching. That is exactly how a unit came back from a venue
// black, fifteen restarts deep, with working HDMI and a connected display.
// Every path that installs the runtime must install the EGL libraries too.
func TestDeployInstallsTheEGLRuntimeSDLDlopens(t *testing.T) {
	for _, script := range []string{"deploy/sd/bake.sh", "deploy/setup.sh"} {
		t.Run(script, func(t *testing.T) {
			s := readDeploy(t, script)
			if !strings.Contains(s, "libsdl2-2.0-0") {
				t.Skip("this script does not install the SDL runtime")
			}
			for _, pkg := range []string{"libegl1", "libegl-mesa0", "libgles2"} {
				if !strings.Contains(s, pkg) {
					t.Errorf("%s installs SDL but not %s — KMSDRM will fail with \"EGL not initialized\"", script, pkg)
				}
			}
		})
	}
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
