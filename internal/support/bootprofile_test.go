package support

import (
	"os"
	"os/exec"
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
		"v4l2-ctl",                     // and what does it say about itself
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the profile has no %q section:\n%s", want, log)
		}
	}
}

// UT-34: a first boot keeps its journal in RAM — the machine id is generated
// during it, so journald will not touch persistent storage — and a sealed card
// keeps every later boot in tmpfs. Either way the ext4 partition is empty and
// the boot the card was pulled for is gone. So the capture snapshots the whole
// boot's journal onto the FAT partition, which survives both: one boot, one
// power-off, and the card carries it.
func TestBootProfileSnapshotsTheJournalToTheBootPartition(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "zeitspiegel-boot-profile.log")
	snap := filepath.Join(dir, "zeitspiegel-journal.log.gz")

	_, stderr, code := collectEnvFrom(t, "deploy/zeitspiegel-boot-profile.sh",
		[]string{"OUT=" + out, "JOURNAL_OUT=" + snap, "PROFILE_WAIT=0"})
	if code != 0 {
		t.Fatalf("the capture aborted: exit %d: %s", code, stderr)
	}

	info, err := os.Stat(snap)
	if err != nil {
		t.Fatalf("no journal snapshot on the boot partition: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("the journal snapshot is empty; a zero-byte file is indistinguishable from a lost one")
	}
	// It has to be readable by whoever receives the card, so it is gzip — and
	// on a machine with no journalctl what lands is that failure, which is
	// itself the answer to "why is there no journal here".
	body, err := exec.Command("gunzip", "-c", snap).Output()
	if err != nil {
		t.Fatalf("the snapshot is not readable gzip: %v", err)
	}
	if len(body) == 0 {
		t.Error("the snapshot decompresses to nothing")
	}
	// The profile names it, so a reader of the profile knows to look.
	profile, _ := os.ReadFile(out)
	if !strings.Contains(string(profile), "zeitspiegel-journal.log.gz") {
		t.Errorf("the profile does not mention the snapshot beside it:\n%s", profile)
	}
}

// UT-34: a unit that has been up for hours and fails at hour six is the case
// a once-at-30-seconds capture cannot answer, however many lines it keeps. The
// timer therefore keeps firing, and the capture decides whether to rewrite:
// once per boot normally — a venue appliance does not write to its card every
// five minutes for nothing — and every firing when a marker on the boot
// partition asks for it, which an operator can drop there by plugging the card
// into a laptop.
func TestBootProfileRewritesOnlyWhenLiveCaptureIsAsked(t *testing.T) {
	run := func(t *testing.T, dir string, extraEnv ...string) string {
		t.Helper()
		out := filepath.Join(dir, "zeitspiegel-boot-profile.log")
		env := append([]string{
			"OUT=" + out,
			"JOURNAL_OUT=" + filepath.Join(dir, "zeitspiegel-journal.log.gz"),
			"CAPTURE_STAMP=" + filepath.Join(dir, "stamp"),
			"PROFILE_WAIT=0",
		}, extraEnv...)
		if _, stderr, code := collectEnvFrom(t, "deploy/zeitspiegel-boot-profile.sh", env); code != 0 {
			t.Fatalf("capture failed: exit %d: %s", code, stderr)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("no capture written: %v", err)
		}
		return string(b)
	}

	t.Run("second firing is a no-op by default", func(t *testing.T) {
		dir := t.TempDir()
		first := run(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "zeitspiegel-boot-profile.log"),
			[]byte(first+"\nSENTINEL\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := run(t, dir); !strings.Contains(got, "SENTINEL") {
			t.Error("the second firing rewrote the capture; a card in a venue would be written to every five minutes")
		}
	})

	t.Run("marker on the boot partition makes it live", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "zeitspiegel-capture-live")
		run(t, dir, "LIVE_MARKER="+marker)
		if err := os.WriteFile(filepath.Join(dir, "zeitspiegel-boot-profile.log"), []byte("SENTINEL\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := run(t, dir, "LIVE_MARKER="+marker); strings.Contains(got, "SENTINEL") {
			t.Error("the marker did not make the capture refresh; a long-running unit stays frozen at 30 s")
		}
	})
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
