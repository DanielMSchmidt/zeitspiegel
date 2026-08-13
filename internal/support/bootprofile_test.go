package support

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// UT-34: the boot profile is the one channel a field unit always has — it
// writes to the FAT32 boot partition, which survives a sealed overlay, a
// missing journal and a unit that never reaches a screen. Its failure mode is
// silence, so this runs it on a machine with no systemd at all: every section
// must still be there, each command's failure captured in place of its output
// rather than aborting the capture.
func TestBootProfileCapturesEverySectionWithoutSystemd(t *testing.T) {
	out := filepath.Join(t.TempDir(), "zeitspiegel-boot-profile.log")

	stdout, stderr, code := collectEnvFrom(t, "deploy/zeitspiegel-boot-profile.sh",
		[]string{"OUT=" + out, "PROFILE_WAIT=0"})
	if code != 0 {
		t.Fatalf("the capture aborted: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the capture wrote no file: %v", err)
	}
	log := string(b)

	for _, want := range []string{
		"zeitspiegel boot profile",
		"systemd-analyze",
		"critical-chain zeitspiegel.service",
		// What a black screen actually needs, none of which the profile
		// carried when a unit first came back dark:
		"systemctl status zeitspiegel", // did it fail, and how many times
		"journalctl -u zeitspiegel",    // the unit's own words, not just the milestones
		"journald storage",             // why the card carries no journal
		"/dev/dri",                     // is there a DRM device for SDL at all
		"drm connectors",               // is anything plugged in and connected
		"/dev/video",                   // did the camera enumerate
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the profile has no %q section:\n%s", want, log)
		}
	}
}

// UT-34: a unit that logged nothing and a journal that cannot be read are
// different diagnoses, and the profile is where they get told apart — an empty
// grep must not be the only thing distinguishing them.
func TestBootProfileSaysWhenTheJournalIsUnreadable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "profile.log")
	if _, _, code := collectEnvFrom(t, "deploy/zeitspiegel-boot-profile.sh",
		[]string{"OUT=" + out, "PROFILE_WAIT=0"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(out)
	// journalctl does not exist here, so its failure is what lands — and it
	// has to land visibly, not be swallowed into an empty section.
	if strings.Contains(string(b), "-- zeitspiegel app: first-frame + http listening (from journal) --\n\n-- ") {
		t.Errorf("a failed journal read is indistinguishable from a silent unit:\n%s", b)
	}
}
