package support

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// checkCaches runs the bake's cache preflight against a cache directory of the
// test's own making — no docker, no network, no 4.8 GB image. It returns what
// the person about to get on a train would read.
func checkCaches(t *testing.T, cacheDir string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", repoPath(t, "scripts/build-image.sh"), "--check-caches")
	cmd.Env = append(os.Environ(), "CACHE_DIR="+cacheDir)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("running build-image.sh --check-caches: %v", err)
	}
	return out.String(), errb.String(), code
}

// warmCache lays down a cache directory with every entry the offline bake
// needs. The contents are stand-ins — the check is about what is present, not
// about what is in it; only the base image is verified byte for byte, and that
// happens against the sidecar written beside it.
func warmCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"raspios-lite-arm64.img.xz":                       "not really an image",
		"apt/archives/ffmpeg_7.1_arm64.deb":               "not really a package",
		"apt/lists/deb.debian.org_dists_trixie_InRelease": "not really a list",
		"gomod/cache/download/lock":                       "",
	})
	return dir
}

// UT-42: a cold cache has to say which cache is cold. The whole point of the
// offline path is that it is checked before the trip, not discovered at the
// venue — so the preflight names every missing piece in one pass rather than
// failing at the first one and making the operator run it four times.
func TestOfflineCacheCheckNamesEveryColdCache(t *testing.T) {
	stdout, stderr, code := checkCaches(t, t.TempDir())
	if code == 0 {
		t.Fatalf("an empty cache directory passed the offline preflight:\n%s%s", stdout, stderr)
	}
	said := stdout + stderr
	for _, want := range []string{
		"raspios-lite-arm64.img.xz", // the base OS download
		"apt/archives",              // the runtime packages the chroot installs
		"apt/lists",                 // without these apt cannot resolve offline
		"gomod",                     // the four modules the cross-build needs
		"make warm-cache",           // and what to do about all of it
	} {
		if !strings.Contains(said, want) {
			t.Errorf("the preflight does not mention %q:\n%s", want, said)
		}
	}
}

// UT-42: and a warm one has to pass, or nobody will trust it enough to rely on
// it. This is the check that stands between "I can bake a card on a plane" and
// "I think I can bake a card on a plane".
func TestOfflineCacheCheckPassesOnAWarmCache(t *testing.T) {
	stdout, stderr, code := checkCaches(t, warmCache(t))
	if code != 0 {
		t.Fatalf("a warm cache failed the offline preflight (exit %d):\n%s%s", code, stdout, stderr)
	}
}

// UT-42: nothing is installed while a card is being baked. Both containers get
// their tools from an image built once (deploy/builder.Dockerfile), so the
// bake path has no apt-get of its own left — a single `apt-get install` in
// either place is a bake that needs the network, and it will be found at the
// worst possible moment.
func TestTheBakeInstallsNoToolsOfItsOwn(t *testing.T) {
	dockerfile := readDeploy(t, "deploy/builder.Dockerfile")
	for _, pkg := range []string{
		// what the cross-build links against
		"libsdl2-dev", "libsdl2-image-dev", "libsdl2-ttf-dev",
		// what bake.sh drives the image with
		"xz-utils", "cloud-guest-utils", "e2fsprogs", "dosfstools", "util-linux", "parted",
	} {
		if !strings.Contains(dockerfile, pkg) {
			t.Errorf("the builder image does not carry %s; the bake would install it every time", pkg)
		}
	}

	makefile := readDeploy(t, "Makefile")
	binary := recipe(t, makefile, "pi-binary:")
	if strings.Contains(binary, "apt-get") {
		t.Errorf("pi-binary installs packages at build time:\n%s", binary)
	}
	if !strings.Contains(binary, "BUILDER_IMAGE") {
		t.Errorf("pi-binary does not use the builder image:\n%s", binary)
	}

	// The chroot's own install is the exception: those packages go into the
	// card, not into a container, and they are cached as .debs instead.
	bake := readDeploy(t, "deploy/sd/bake.sh")
	for _, line := range strings.Split(bake, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, "apt-get install") {
			continue
		}
		if !strings.Contains(trimmed, `chroot "$ROOT"`) {
			t.Errorf("the bake container installs its own tools: %q", trimmed)
		}
	}
}

// UT-42: the cross-build keeps its module and build caches outside the
// container, where they survive it. Without this every bake re-downloads four
// modules and recompiles the standard library for arm64.
func TestTheCrossBuildCachesOutliveItsContainer(t *testing.T) {
	binary := recipe(t, readDeploy(t, "Makefile"), "pi-binary:")
	for _, want := range []string{"/go/pkg/mod", "/root/.cache/go-build"} {
		if !strings.Contains(binary, want) {
			t.Errorf("pi-binary does not mount a cache at %s:\n%s", want, binary)
		}
	}
}

// UT-42: offline, apt must not reach for the network — and must not silently
// carry on when a package it needs was never cached. `--no-download` is what
// turns a missing .deb into a refusal instead of a card baked without ffmpeg.
func TestTheChrootInstallCanRunOffline(t *testing.T) {
	bake := readDeploy(t, "deploy/sd/bake.sh")
	offline, online := offlineBranch(t, bake)

	// --no-download is what turns a package that was never cached into a
	// refusal rather than a card baked without ffmpeg.
	if !strings.Contains(offline, "--no-download") {
		t.Error("the offline branch still lets apt download; OFFLINE=1 would need a network")
	}
	// apt-get update is a download by definition, so it belongs to the online
	// branch and nowhere else — an unconditional one is the whole feature
	// undone.
	if strings.Contains(offline, "apt-get update") {
		t.Error("the offline branch runs apt-get update")
	}
	if got, want := strings.Count(bake, "apt-get update"), strings.Count(online, "apt-get update"); got != want {
		t.Errorf("bake.sh runs apt-get update %d times but only %d are in the online branch; the rest are unconditional", got, want)
	}

	// apt-get clean empties /var/cache/apt/archives — which, while the host's
	// cache is bind-mounted there, is the cache. It has to be released between
	// the install and the clean, or the bake wipes what it just filled.
	install, clean, release := -1, -1, -1
	for i, line := range strings.Split(bake, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"):
		case install < 0 && strings.Contains(trimmed, "--no-download"):
			install = i
		case install >= 0 && release < 0 && trimmed == "release_apt_cache":
			release = i
		case clean < 0 && strings.Contains(trimmed, "apt-get clean"):
			clean = i
		}
	}
	if install < 0 {
		t.Fatal("no offline install to sequence around")
	}
	if release < 0 {
		t.Fatal("nothing releases the apt cache mount after the install; the bake would leave it mounted into apt-get clean")
	}
	if clean >= 0 && clean < release {
		t.Error("apt-get clean runs while the .deb cache is still mounted — the next offline bake would find it empty")
	}
}

// offlineBranch returns the two halves of bake.sh's `if [[ "$OFFLINE" == 1 ]]`
// block: what a bake with no network does, and what one with a network does.
func offlineBranch(t *testing.T, bake string) (offline, online string) {
	t.Helper()
	const open = `if [[ "$OFFLINE" == 1 ]]; then`
	i := strings.Index(bake, open)
	if i < 0 {
		t.Fatal("bake.sh has no offline branch; OFFLINE=1 would bake exactly the same way")
	}
	rest := bake[i+len(open):]
	end := strings.Index(rest, "\nfi\n")
	if end < 0 {
		t.Fatal("bake.sh's offline branch is not closed")
	}
	body := rest[:end]
	split := strings.Index(body, "\nelse\n")
	if split < 0 {
		t.Fatal("bake.sh's offline branch has no online half")
	}
	return body[:split], body[split:]
}

// recipe returns the body of a Makefile target: the lines under `name` up to
// the first line that is neither indented nor blank.
func recipe(t *testing.T, makefile, name string) string {
	t.Helper()
	lines := strings.Split(makefile, "\n")
	var body []string
	for i, line := range lines {
		if !strings.HasPrefix(line, name) {
			continue
		}
		for _, l := range lines[i+1:] {
			if l != "" && !strings.HasPrefix(l, "\t") && !strings.HasPrefix(l, " ") {
				break
			}
			body = append(body, l)
		}
		return strings.Join(body, "\n")
	}
	t.Fatalf("no %s target in the Makefile", name)
	return ""
}
