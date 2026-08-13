package support

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRoot builds a filesystem root carrying the given shared objects, the way
// a baked image carries them under /usr/lib/<triplet>.
func fakeRoot(t *testing.T, sonames ...string) string {
	t.Helper()
	root := t.TempDir()
	libdir := filepath.Join(root, "usr/lib/aarch64-linux-gnu")
	if err := os.MkdirAll(libdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, so := range sonames {
		if err := os.WriteFile(filepath.Join(libdir, so), []byte("\x7fELF"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", so, err)
		}
	}
	return root
}

func requiredSonames(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(repoPath(t, "deploy/runtime-libs.txt"))
	if err != nil {
		t.Fatalf("reading the runtime library list: %v", err)
	}
	var libs []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "<soname> <package> # why" — the soname is what a root carries.
		libs = append(libs, strings.Fields(line)[0])
	}
	if len(libs) == 0 {
		t.Fatal("the runtime library list is empty; nothing would ever be checked")
	}
	return libs
}

// UT-37: the appliance's display path loads libEGL.so.1 with dlopen, so no
// linker, no build and no test on any tier can notice when the image does not
// have it — which is how a card shipped for two months with a binary that
// could never open a screen. The check that closes that gap has to fail
// loudly and name the file, and it runs against a root directory so the bake
// can ask it about an image it has not booted.
func TestCheckRuntimeFindsAMissingLibrary(t *testing.T) {
	libs := requiredSonames(t)

	t.Run("all present", func(t *testing.T) {
		root := fakeRoot(t, libs...)
		stdout, stderr, code := collectFrom(t, "deploy/check-runtime.sh", root)
		if code != 0 {
			t.Fatalf("a complete root was rejected: exit %d\n%s%s", code, stdout, stderr)
		}
	})

	t.Run("one missing", func(t *testing.T) {
		// Drop exactly the one that was actually missing in the field.
		var kept []string
		for _, so := range libs {
			if so != "libEGL.so.1" {
				kept = append(kept, so)
			}
		}
		if len(kept) == len(libs) {
			t.Fatal("libEGL.so.1 is not in the runtime list; the field failure would not be caught")
		}
		root := fakeRoot(t, kept...)
		stdout, stderr, code := collectFrom(t, "deploy/check-runtime.sh", root)
		out := stdout + stderr
		if code == 0 {
			t.Fatalf("a root with no libEGL.so.1 passed:\n%s", out)
		}
		if !strings.Contains(out, "libEGL.so.1") {
			t.Errorf("the failure does not name the missing library:\n%s", out)
		}
		// The reason it is needed travels with it, so whoever hits this knows
		// what to install rather than what to delete.
		if !strings.Contains(out, "libegl1") {
			t.Errorf("the failure does not name the package that provides it:\n%s", out)
		}
	})
}

// UT-37: one list, used by every path that installs the runtime. The image
// bake and the on-a-Pi installer drifting apart is what let a third,
// unexercised dependency set exist at all.
func TestInstallPathsShareOneRuntimeList(t *testing.T) {
	for _, script := range []string{"deploy/sd/bake.sh", "deploy/setup.sh"} {
		s := readDeploy(t, script)
		if !strings.Contains(s, "runtime-packages.txt") {
			t.Errorf("%s does not install from the shared package list", script)
		}
		if !strings.Contains(s, "check-runtime.sh") {
			t.Errorf("%s installs the runtime without verifying it afterwards", script)
		}
	}
	pkgs, err := os.ReadFile(repoPath(t, "deploy/runtime-packages.txt"))
	if err != nil {
		t.Fatalf("reading the package list: %v", err)
	}
	for _, want := range []string{"libegl1", "libegl-mesa0", "libgles2", "libsdl2-2.0-0", "ffmpeg"} {
		if !strings.Contains(string(pkgs), want) {
			t.Errorf("the shared package list is missing %s", want)
		}
	}
}
