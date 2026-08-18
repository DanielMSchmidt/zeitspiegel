package support

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sizeCheck runs the flashing script's size verdict — the lines it prints
// above the "type erase" prompt — against an image and a disk given as a byte
// count, so the check can be exercised without a card in the reader.
func sizeCheck(t *testing.T, img, diskBytes string) (stdout, stderr string, code int) {
	t.Helper()
	return collectFrom(t, "scripts/flash-sd.sh", "--size-check", img, diskBytes)
}

// UT-33: the confirmation prompt is the last moment before a card is erased,
// so it has to answer the question being asked at it — is this card big enough
// for this image? A card that is too small is refused there rather than
// erased and then failed on halfway through dd, which costs the card's
// contents for nothing.
func TestFlashSizeCheckAnswersWhetherTheCardFits(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "appliance.img")
	// 4 MiB stands in for the real 4.6 GB image: what matters is the
	// comparison and how the number reads, not the magnitude.
	if err := os.WriteFile(img, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatalf("writing fixture image: %v", err)
	}

	for _, tc := range []struct {
		name      string
		diskBytes string
		wantFits  bool
	}{
		{"roomy card", "32000000000", true},
		{"exactly the image size", "4194304", true},
		{"one byte short", "4194303", false},
		{"far too small", "1000000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := sizeCheck(t, img, tc.diskBytes)
			out := stdout + stderr
			if fits := code == 0; fits != tc.wantFits {
				t.Fatalf("fits=%v, want %v (exit %d)\n%s", fits, tc.wantFits, code, out)
			}
			// The required size is stated either way — that is the number the
			// operator came to the prompt for.
			if !strings.Contains(out, "4.2 MB") && !strings.Contains(out, "4 MB") {
				t.Errorf("the image size is not shown in human terms:\n%s", out)
			}
			if !tc.wantFits && !strings.Contains(strings.ToUpper(out), "TOO SMALL") {
				t.Errorf("a card that cannot hold the image is not called too small:\n%s", out)
			}
		})
	}
}

// UT-38: `make sd` re-bakes, so a fresh image is verified on the way past —
// but the flasher will happily write any image that happens to be in build/,
// including one baked before the runtime check existed. That is the image that
// produces a black screen, so the card writer refuses it: the bake records its
// verdict on the boot partition and flashing reads it back.
func TestFlashRefusesAnImageThatWasNeverVerified(t *testing.T) {
	verified := writeTree(t, filepath.Join(t.TempDir(), "bootfs"), map[string]string{
		"zeitspiegel-version.txt": "version=v1.4.2\nruntime_check=ok (4 libraries)\n",
	})
	stale := writeTree(t, filepath.Join(t.TempDir(), "bootfs"), map[string]string{
		"zeitspiegel-version.txt": "version=v1.0.0\n",
	})

	t.Run("verified image passes", func(t *testing.T) {
		stdout, stderr, code := collectFrom(t, "scripts/flash-sd.sh", "--check-bootfs", verified)
		if code != 0 {
			t.Fatalf("a verified image was refused: exit %d\n%s%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "4 libraries") {
			t.Errorf("the verdict is not shown to the operator:\n%s%s", stdout, stderr)
		}
	})

	t.Run("unverified image is refused, with a way through", func(t *testing.T) {
		_, stderr, code := collectFrom(t, "scripts/flash-sd.sh", "--check-bootfs", stale)
		if code == 0 {
			t.Fatal("an image that never passed the runtime check was accepted")
		}
		for _, want := range []string{"make image", "ALLOW_UNVERIFIED_IMAGE"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("the refusal does not say %q, so nobody knows what to do: %s", want, stderr)
			}
		}
		// The override exists because an old image is sometimes exactly what
		// you want to write — bisecting a regression, for one.
		_, _, code = collectEnvFrom(t, "scripts/flash-sd.sh",
			[]string{"ALLOW_UNVERIFIED_IMAGE=1"}, "--check-bootfs", stale)
		if code != 0 {
			t.Errorf("ALLOW_UNVERIFIED_IMAGE did not let the image through (exit %d)", code)
		}
	})
}

// UT-33: a missing image must not read as a card problem.
func TestFlashSizeCheckNeedsAnImage(t *testing.T) {
	_, stderr, code := sizeCheck(t, filepath.Join(t.TempDir(), "absent.img"), "32000000000")
	if code == 0 {
		t.Fatalf("a missing image passed the size check")
	}
	if !strings.Contains(stderr, "absent.img") {
		t.Errorf("the error does not name the missing image: %s", stderr)
	}
}

// mediaCheck runs the flashing script's write-protect verdict against captured
// `diskutil info` output, so the decision can be exercised without a locked
// card in the reader.
func mediaCheck(t *testing.T, diskutilInfo string) (stdout, stderr string, code int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "diskutil-info.txt")
	if err := os.WriteFile(path, []byte(diskutilInfo), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return collectFrom(t, "scripts/flash-sd.sh", "--media-check", path)
}

// UT-33: a write-protected card cannot be written to by anyone, root included
// — the kernel refuses the raw write with EPERM, which surfaces as
// "dd: /dev/rdiskN: Permission denied" and reads like a sudo problem. Catch it
// at the prompt, where the card still has its contents and the operator can
// reach the lock switch.
func TestFlashMediaCheckRefusesWriteProtectedMedia(t *testing.T) {
	const locked = `   Device Identifier:        disk4
   Device / Media Name:      Built In SDXC Reader
   Media Read-Only:          Yes
   Volume Read-Only:         Not applicable (no file system)
`
	// Older macOS spells the same fact the other way round.
	const lockedOldSpelling = `   Device Identifier:        disk4
   Read-Only Media:          Yes
`
	// A read-only *volume* is a different condition — a mount option, not the
	// media — and is no reason to refuse a write that erases the volume anyway.
	const readOnlyVolume = `   Device Identifier:        disk4
   Media Read-Only:          No
   Volume Read-Only:         Yes
`
	const writable = `   Device Identifier:        disk4
   Media Read-Only:          No
   Volume Read-Only:         No
`
	for _, tc := range []struct {
		name       string
		info       string
		wantRefuse bool
	}{
		{"locked card", locked, true},
		{"locked card, older field name", lockedOldSpelling, true},
		{"read-only volume on writable media", readOnlyVolume, false},
		{"ordinary card", writable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := mediaCheck(t, tc.info)
			out := stdout + stderr
			if refused := code != 0; refused != tc.wantRefuse {
				t.Fatalf("refused=%v, want %v (exit %d)\n%s", refused, tc.wantRefuse, code, out)
			}
			if !tc.wantRefuse {
				return
			}
			if !strings.Contains(strings.ToLower(out), "write-protect") {
				t.Errorf("the refusal does not name the condition:\n%s", out)
			}
			// The operator has to be told where to look; "write-protected"
			// alone leaves them checking sudo and Full Disk Access instead.
			if !strings.Contains(strings.ToLower(out), "lock switch") {
				t.Errorf("the refusal does not point at the lock switch:\n%s", out)
			}
			if !strings.Contains(out, "disk4") {
				t.Errorf("the refusal does not name the disk:\n%s", out)
			}
		})
	}
}
