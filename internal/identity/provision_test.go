package identity_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmschmidt/zeitspiegel/internal/identity"
)

// stageName runs the provisioning script the way `make sd` does. It returns
// stdout, stderr and the exit code, so a test can assert both the refusal and
// the wording a human reads at the card writer.
func stageName(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"../../scripts/stage-name.sh"}, args...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	var exit *exec.ExitError
	switch err := cmd.Run(); {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("running stage-name.sh: %v", err)
	}
	return out.String(), errb.String(), code
}

// resolveStaged reads the staged card the way the appliance does at boot: the
// name file lives on the boot partition, under the name the identity package
// looks for.
func resolveStaged(t *testing.T, bootfs string) identity.Unit {
	t.Helper()
	dir := t.TempDir()
	u, _ := identity.Resolve(identity.Sources{
		CPUInfoPath: writeFile(t, dir, "cpuinfo", piCPUInfo),
		NameFile:    filepath.Join(bootfs, filepath.Base(identity.DefaultNameFile)),
	})
	return u
}

// UT-31: a card named at flashing time boots with that name — the script
// writes the very file the identity package reads, so the label the operator
// typed into `make sd` is the label the UI shows.
func TestStageNameIsWhatTheUnitReadsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{"plain", "Long Side", "Long Side"},
		{"spaces inside", "Window seat", "Window seat"},
		{"surrounding space trimmed", "  Long Side  ", "Long Side"},
		{"umlauts survive", "Spiegel Süd", "Spiegel Süd"},
		{"at the length limit", strings.Repeat("x", identity.MaxNameLen), strings.Repeat("x", identity.MaxNameLen)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootfs := t.TempDir()
			if _, errOut, code := stageName(t, tc.arg, bootfs); code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, errOut)
			}
			if got := resolveStaged(t, bootfs).Name; got != tc.want {
				t.Fatalf("Name = %q, want %q", got, tc.want)
			}
		})
	}
}

// UT-31: without a target directory the script only validates, so `make sd`
// can reject a bad label in the first second instead of after a five-minute
// image bake and card write.
func TestStageNameValidatesWithoutWriting(t *testing.T) {
	out, errOut, code := stageName(t, "Long Side")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if strings.TrimSpace(out) != "Long Side" {
		t.Fatalf("stdout = %q, want the cleaned name", out)
	}
}

// UT-31: a label the appliance would silently drop or mangle is refused at the
// card writer, where the operator can still retype it. Nothing is written on a
// refusal — a half-named card is worse than an unnamed one.
func TestStageNameRefusesUnusableLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{"empty", ""},
		{"whitespace only", "   \t "},
		{"second line would be dropped", "Long Side\nignored"},
		{"leading blank line", "\nLong Side"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootfs := t.TempDir()
			_, errOut, code := stageName(t, tc.arg, bootfs)
			if code == 0 {
				t.Fatalf("staged %q, want a refusal", tc.arg)
			}
			if errOut == "" {
				t.Fatal("refused silently; the operator needs to know why")
			}
			if entries, err := os.ReadDir(bootfs); err != nil || len(entries) != 0 {
				t.Fatalf("bootfs entries = %v (err %v), want nothing written", entries, err)
			}
		})
	}
}

// UT-31: an over-long label is truncated by the appliance, not rejected
// (identity.MaxNameLen). The script keeps that behavior but says so, so the
// operator is not surprised by the name on the screen.
func TestStageNameWarnsOnTruncation(t *testing.T) {
	bootfs := t.TempDir()
	long := strings.Repeat("x", identity.MaxNameLen+10)
	_, errOut, code := stageName(t, long, bootfs)
	if code != 0 {
		t.Fatalf("exit %d, want the long name accepted; stderr: %s", code, errOut)
	}
	if !strings.Contains(strings.ToLower(errOut), "truncat") {
		t.Fatalf("stderr = %q, want a truncation warning", errOut)
	}
	if got, want := resolveStaged(t, bootfs).Name, strings.Repeat("x", identity.MaxNameLen); got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
}

// UT-31: macOS likes to mount a freshly written FAT32 boot partition
// read-only, and a bare shell redirect reports that as
// "line 59: /Volumes/bootfs/zeitspiegel-name.txt: Read-only file system" —
// which says nothing about what to do. Diagnose it before writing, name the
// partition, and say how to fix it.
func TestStageNameReportsAnUnwritableBootfs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits this test relies on")
	}
	bootfs := t.TempDir()
	if err := os.Chmod(bootfs, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bootfs, 0o755) })

	_, errOut, code := stageName(t, "Long Side", bootfs)
	if code == 0 {
		t.Fatal("staged onto a read-only bootfs, want a refusal")
	}
	if !strings.Contains(errOut, bootfs) {
		t.Errorf("stderr = %q, want the partition it could not write to", errOut)
	}
	if !strings.Contains(errOut, "cannot write to") {
		t.Errorf("stderr = %q, want the problem stated in the operator's terms", errOut)
	}
	// A raw redirect failure names the script and a line number; a diagnosed
	// one does not.
	if strings.Contains(errOut, "line ") {
		t.Errorf("stderr = %q, want a diagnosed error, not a shell redirect failure", errOut)
	}
	if entries, err := os.ReadDir(bootfs); err != nil || len(entries) != 0 {
		t.Fatalf("bootfs entries = %v (err %v), want nothing written", entries, err)
	}
}

// UT-31: `NAME=auto` is the documented way to ship a deliberately unnamed
// card. Nothing lands on the boot partition and the unit falls back to naming
// itself after its id — the image stays byte-identical either way (E-8).
func TestStageNameAutoLeavesTheCardUnnamed(t *testing.T) {
	bootfs := t.TempDir()
	if _, errOut, code := stageName(t, "auto", bootfs); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if entries, err := os.ReadDir(bootfs); err != nil || len(entries) != 0 {
		t.Fatalf("bootfs entries = %v (err %v), want nothing written", entries, err)
	}
	u := resolveStaged(t, bootfs)
	if u.Name != identity.DefaultName(u.ID) {
		t.Fatalf("Name = %q, want the id-derived default %q", u.Name, identity.DefaultName(u.ID))
	}
}

// UT-31: re-naming a card that already carries a name replaces the label
// rather than appending to it — the appliance reads only the first line, so a
// stale first line would win forever.
func TestStageNameReplacesAnExistingLabel(t *testing.T) {
	bootfs := t.TempDir()
	if _, errOut, code := stageName(t, "Long Side", bootfs); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if _, errOut, code := stageName(t, "Window seat", bootfs); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if got := resolveStaged(t, bootfs).Name; got != "Window seat" {
		t.Fatalf("Name = %q, want the new label", got)
	}
}
