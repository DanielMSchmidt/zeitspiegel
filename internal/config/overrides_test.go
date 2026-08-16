package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/config"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// UT-47 (FR-18): a setting changed through the API survives the restart. The
// unit is unplugged rather than shut down, so "until the next power cut" is
// the same as "not at all" to anyone using it.
func TestOverridesRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")

	if err := config.SaveOverrides(p, config.Patch{MirrorFlip: ptr(true), BufferMaxS: ptr(45.0)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := config.LoadOverrides(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.MirrorFlip == nil || !*got.MirrorFlip {
		t.Errorf("mirror_flip = %v, want true", got.MirrorFlip)
	}
	if got.BufferMaxS == nil || *got.BufferMaxS != 45 {
		t.Errorf("buffer_max_s = %v, want 45", got.BufferMaxS)
	}
	if got.Profile != nil {
		t.Errorf("profile = %v, want unset — nobody set it", *got.Profile)
	}
}

// UT-47: the file is a patch, not a snapshot. A key nobody touched must stay
// absent, so editing deploy/config.toml on the card still changes what that
// unit boots with — a stored full config would mask every later default.
func TestOverridesFileCarriesOnlyWhatWasSet(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := config.SaveOverrides(p, config.Patch{MirrorFlip: ptr(false)}); err != nil {
		t.Fatalf("save: %v", err)
	}
	body := read(t, p)
	if !strings.Contains(body, "mirror_flip") {
		t.Errorf("file %q does not carry the value that was set", body)
	}
	for _, key := range []string{"profile", "buffer_max_s", "focus_auto", "exposure_absolute"} {
		if strings.Contains(body, key) {
			t.Errorf("file %q carries %q, which nobody set", body, key)
		}
	}
	// And it stays readable by a person with the card in a reader.
	var any map[string]any
	if err := json.Unmarshal([]byte(body), &any); err != nil {
		t.Fatalf("file is not JSON an operator can edit: %v", err)
	}
}

// UT-47: a unit that has never been touched has no file, and that is the
// ordinary first boot — not a fault to log or fail on.
func TestOverridesMissingFileIsNotAnError(t *testing.T) {
	got, err := config.LoadOverrides(filepath.Join(t.TempDir(), "never-written.json"))
	if err != nil {
		t.Fatalf("missing overrides file: %v, want no error", err)
	}
	if (got != config.Patch{}) {
		t.Errorf("missing file yielded %+v, want an empty patch", got)
	}
}

// UT-47: no path configured ⇒ persistence is off, both ways. That is the
// laptop case (`make run-synth`), where /boot/firmware does not exist and an
// error per PATCH would be noise.
func TestOverridesNoPathIsANoop(t *testing.T) {
	if err := config.SaveOverrides("", config.Patch{MirrorFlip: ptr(true)}); err != nil {
		t.Errorf("save with no path: %v, want no error", err)
	}
	got, err := config.LoadOverrides("")
	if err != nil {
		t.Errorf("load with no path: %v, want no error", err)
	}
	if (got != config.Patch{}) {
		t.Errorf("load with no path yielded %+v, want an empty patch", got)
	}
}

// UT-47: a hand-edited or half-written file names itself in the error. The
// caller boots on the config file's values instead — a mirror must not be
// bricked by a stray byte on its boot partition.
func TestOverridesCorruptFileIsAnError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"mirror_flip": tr`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadOverrides(p)
	if err == nil {
		t.Fatal("corrupt overrides file: want an error")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error %q does not name the file", err)
	}
}

// UT-47: the write lands whole or not at all (NFR-9). This file lives on the
// FAT32 boot partition of a unit that is switched off by pulling its plug, and
// FAT has no journal: a half-written settings file must not be possible, and
// no scratch file may be left behind on the partition the Pi boots from.
func TestOverridesSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	if err := config.SaveOverrides(p, config.Patch{MirrorFlip: ptr(true)}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := config.SaveOverrides(p, config.Patch{MirrorFlip: ptr(false), Profile: ptr("720p60")}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(p) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only %q", names, filepath.Base(p))
	}
	got, err := config.LoadOverrides(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.MirrorFlip == nil || *got.MirrorFlip || got.Profile == nil || *got.Profile != "720p60" {
		t.Errorf("second save did not replace the first: %+v", got)
	}
}

// UT-47: an unwritable location is reported rather than swallowed — the whole
// point of the file is that somebody is relying on it.
func TestOverridesSaveReportsAnUnwritableLocation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "no-such-dir", "settings.json")
	if err := config.SaveOverrides(p, config.Patch{MirrorFlip: ptr(true)}); err == nil {
		t.Fatal("save into a missing directory: want an error")
	}
}

// UT-47: patches accumulate. The stored patch is everything ever set on this
// unit, so turning the flip off and later switching the profile keeps both.
func TestPatchMerge(t *testing.T) {
	base := config.Patch{MirrorFlip: ptr(false), Profile: ptr("auto")}
	got := base.Merge(config.Patch{Profile: ptr("720p60"), FocusAbsolute: ptr(30)})

	if got.MirrorFlip == nil || *got.MirrorFlip {
		t.Errorf("mirror_flip = %v, want the earlier false to survive", got.MirrorFlip)
	}
	if got.Profile == nil || *got.Profile != "720p60" {
		t.Errorf("profile = %v, want the later value to win", got.Profile)
	}
	if got.FocusAbsolute == nil || *got.FocusAbsolute != 30 {
		t.Errorf("focus_absolute = %v, want 30", got.FocusAbsolute)
	}
	if base.Profile == nil || *base.Profile != "auto" {
		t.Error("Merge mutated its receiver")
	}
}

// UT-47: the effective runtime folds back into the boot config, so everything
// downstream of it — the ring buffer's budget, the camera open, the display's
// flip — sees what the unit is actually running under rather than what the
// file booted with.
func TestWithRuntimeFoldsBackIntoTheBootConfig(t *testing.T) {
	c := config.Default()
	c.MirrorFlip = true
	c.BufferMaxS = 120
	c.Bind = ":8080"

	rt, err := c.Runtime().WithPatch(config.Patch{MirrorFlip: ptr(false), BufferMaxS: ptr(45.0)})
	if err != nil {
		t.Fatal(err)
	}
	got := c.WithRuntime(rt)

	if got.MirrorFlip || got.BufferMaxS != 45 {
		t.Errorf("runtime values not folded back: mirror_flip=%v buffer_max_s=%v", got.MirrorFlip, got.BufferMaxS)
	}
	if got.Bind != ":8080" || got.Source != c.Source {
		t.Errorf("a key outside the runtime subset was touched: %+v", got)
	}
	if got.Runtime() != rt {
		t.Errorf("Runtime() = %+v after folding, want %+v", got.Runtime(), rt)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("folded config invalid: %v", err)
	}
}
