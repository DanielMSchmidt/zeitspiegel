// Package support tests the field-support tooling — scripts an operator runs
// on a laptop with a card in the reader, not code that ships on the unit.
package support

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// collect runs the collector the way an operator does, in the offline mode
// that takes trees instead of a disk: no diskutil, no sudo, no card. It
// returns stdout, stderr and the exit code, because the wording a human reads
// at the card reader is as much the deliverable as the bundle is.
func collect(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{repoPath(t, "scripts/collect-sd-logs.sh")}, args...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("running collect-sd-logs.sh: %v", err)
	}
	return out.String(), errb.String(), code
}

func repoPath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("../..", rel))
	if err != nil {
		t.Fatalf("resolving %s: %v", rel, err)
	}
	return abs
}

// writeTree materialises a fixture card as plain directories — what a mounted
// bootfs and a mounted (or debugfs-extracted) rootfs look like.
func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return root
}

const (
	debugLog = `==========================================
STAGE: pre-rfkill   uptime 3.41s
-- /sys/class/rfkill/* (KERNEL view, can't lie about driver state) --
    soft         1
`
	bootProfile  = "-- zeitspiegel app: first-frame + http listening (from journal) --\nfirst frame after 9.2s\n"
	cmdline      = "console=serial0,115200 root=PARTUUID=deadbeef-02 rootfstype=ext4 boot=overlay cfg80211.ieee80211_regdom=DE quiet loglevel=0\n"
	apProfile    = "[connection]\nid=zeitspiegel-ap\n[wifi-security]\nkey-mgmt=none\npsk=hunter2-supersecret\n"
	userconf     = "zeitspiegel:$6$saltsalt$V3rySecretHashDoNotShip.\n"
	journalBytes = "LPKSHHRH\x00\x00\x00\x00zeitspiegel journal payload\n"
)

func fullCard(t *testing.T) (bootfs, rootfs string) {
	t.Helper()
	dir := t.TempDir()
	bootfs = writeTree(t, filepath.Join(dir, "bootfs"), map[string]string{
		"zeitspiegel-name.txt":         "Long Side\n",
		"zeitspiegel-debug.log":        debugLog,
		"zeitspiegel-boot-profile.log": bootProfile,
		"cmdline.txt":                  cmdline,
		"config.txt":                   "dtoverlay=disable-bt\n",
		"userconf.txt":                 userconf,
		"zeitspiegel-authorized_keys":  "ssh-ed25519 AAAAC3Nza operator@laptop\n",
	})
	rootfs = writeTree(t, filepath.Join(dir, "rootfs"), map[string]string{
		"etc/os-release":              "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n",
		"etc/hostname":                "zeitspiegel\n",
		"etc/zeitspiegel/config.toml": "buffer_max_s = 60\nnetwork_manage = true\n",
		"etc/NetworkManager/system-connections/zeitspiegel-ap.nmconnection": apProfile,
		"var/lib/NetworkManager/NetworkManager.state":                       "[main]\nWirelessEnabled=true\n",
		"var/lib/systemd/rfkill":                                            "",
		"var/log/journal/9f2c/system.journal":                               journalBytes,
		"var/log/daemon.log":                                                "Jun 13 10:00:01 zeitspiegel systemd[1]: Started zeitspiegel.service\n",
	})
	return bootfs, rootfs
}

// artifact asserts the run left exactly one thing behind — the zip — and
// returns it. One call, one file: an operator with three cards and a bug
// report to file must not have to work out which of several outputs to
// attach, and no scratch directory may be left beside it.
func artifact(t *testing.T, out string) string {
	t.Helper()
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("reading %s: %v", out, err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected exactly one artifact in %s, got %v", out, names)
	}
	if !strings.HasSuffix(entries[0].Name(), ".zip") {
		t.Fatalf("the artifact is not a zip: %s", entries[0].Name())
	}
	return filepath.Join(out, entries[0].Name())
}

// unpack extracts the artifact, which is how anybody receiving it reads it.
func unpack(t *testing.T, zip string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("unzip", "-q", zip, "-d", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unzip %s: %v\n%s", zip, err, out)
	}
	return dir
}

// bundleText returns every readable byte the operator would hand over. A
// secret that leaks into any file inside the zip has leaked.
func bundleText(t *testing.T, unpacked string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(unpacked, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		b.WriteString(p)
		b.WriteByte('\n')
		b.Write(body)
		b.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatalf("walking bundle: %v", err)
	}
	return b.String()
}

func reportPath(t *testing.T, unpacked string) string {
	t.Helper()
	hits, err := filepath.Glob(filepath.Join(unpacked, "zeitspiegel-logs-*", "report.txt"))
	if err != nil || len(hits) != 1 {
		t.Fatalf("expected exactly one report in the zip, got %v (%v)", hits, err)
	}
	return hits[0]
}

func readReport(t *testing.T, unpacked string) string {
	t.Helper()
	b, err := os.ReadFile(reportPath(t, unpacked))
	if err != nil {
		t.Fatalf("reading report: %v", err)
	}
	return string(b)
}

// UT-32: the bundle is the whole story of one card. Both halves of the card
// are in it — the FAT32 evidence that survives a sealed overlay and the
// persistent journal on the ext4 root — and it collapses into the single zip
// that is the run's only output.
func TestCollectGathersBothPartitionsIntoOneFile(t *testing.T) {
	bootfs, rootfs := fullCard(t)
	out := t.TempDir()

	stdout, stderr, code := collect(t, "--bootfs", bootfs, "--rootfs", rootfs, "--out", out)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	zip := artifact(t, out)
	unpacked := unpack(t, zip)
	report := readReport(t, unpacked)
	for _, want := range []string{
		"Long Side",                   // the card names itself in the header
		"STAGE: pre-rfkill",           // boot-partition rfkill evidence
		"first frame after 9.2s",      // boot profile
		"cfg80211.ieee80211_regdom",   // kernel cmdline, regdomain and all
		"buffer_max_s = 60",           // the config the unit actually ran
		"WirelessEnabled=true",        // NetworkManager's own enable gate
		"Started zeitspiegel.service", // plain-text rootfs logs
		"system.journal",              // the journal is accounted for by name
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q\n---\n%s", want, report)
		}
	}

	// The journal is binary; it is carried verbatim so it can be rendered with
	// journalctl -D later, not summarised away.
	copied, err := filepath.Glob(filepath.Join(unpacked, "zeitspiegel-logs-*", "rootfs", "var", "log", "journal", "*", "system.journal"))
	if err != nil || len(copied) != 1 {
		t.Fatalf("journal not carried in the zip: %v (%v)", copied, err)
	}
	if b, err := os.ReadFile(copied[0]); err != nil || string(b) != journalBytes {
		t.Errorf("journal copy differs from the card: %q (%v)", b, err)
	}

	// The zip is what the operator attaches, so its path is the last word.
	if !strings.Contains(stdout, zip) {
		t.Errorf("stdout does not name the artifact %s:\n%s", zip, stdout)
	}
}

// UT-32: a bundle travels — into a chat, an issue, an email. The Wi-Fi
// pre-shared key and the admin password hash never travel with it, while the
// keys themselves stay visible so a reader still knows the profile existed.
func TestCollectRedactsWhatMustNotTravel(t *testing.T) {
	bootfs, rootfs := fullCard(t)
	out := t.TempDir()

	if _, stderr, code := collect(t, "--bootfs", bootfs, "--rootfs", rootfs, "--out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	all := bundleText(t, unpack(t, artifact(t, out)))
	for _, secret := range []string{"hunter2-supersecret", "V3rySecretHashDoNotShip"} {
		if strings.Contains(all, secret) {
			t.Errorf("secret %q leaked into the bundle", secret)
		}
	}
	for _, want := range []string{"psk=", "zeitspiegel-ap.nmconnection", "key-mgmt=none"} {
		if !strings.Contains(all, want) {
			t.Errorf("redaction removed the evidence too: %q is gone", want)
		}
	}
	if !strings.Contains(all, "REDACTED") {
		t.Errorf("redaction is silent — a reader cannot tell a value was removed:\n%s", all)
	}
}

// UT-32: the cards that need collecting are the broken ones. A card that never
// got as far as writing a debug log, or whose root partition cannot be read on
// this machine (macOS without an ext4 reader), still produces a report saying
// so by name rather than an empty file or a shell error.
func TestCollectSurvivesAHalfWrittenCard(t *testing.T) {
	dir := t.TempDir()
	bootfs := writeTree(t, filepath.Join(dir, "bootfs"), map[string]string{
		"cmdline.txt": cmdline,
	})
	out := t.TempDir()

	stdout, stderr, code := collect(t, "--bootfs", bootfs, "--out", out)
	if code != 0 {
		t.Fatalf("a partial card must still yield a bundle: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	report := readReport(t, unpack(t, artifact(t, out)))
	for _, want := range []string{"zeitspiegel-debug.log", "not present", "root filesystem"} {
		if !strings.Contains(report, want) {
			t.Errorf("report does not account for what is missing (%q):\n%s", want, report)
		}
	}
}

// UT-32: pointing the collector at the wrong volume — a camera card, a stick
// of holiday photos — must fail loudly. Silently bundling a stranger's files
// is both a privacy leak and a debugging dead end.
func TestCollectRefusesAForeignCard(t *testing.T) {
	dir := t.TempDir()
	bootfs := writeTree(t, filepath.Join(dir, "NIKON"), map[string]string{
		"DCIM/DSC_0001.JPG": "\xff\xd8\xff\xe0not a mirror",
	})
	out := t.TempDir()

	_, stderr, code := collect(t, "--bootfs", bootfs, "--out", out)
	if code == 0 {
		t.Fatalf("a foreign volume was collected instead of refused")
	}
	if !strings.Contains(stderr, bootfs) {
		t.Errorf("the refusal does not name the volume it looked at: %s", stderr)
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 0 {
		t.Errorf("a refused card still left %v behind (%v)", entries, err)
	}
}
