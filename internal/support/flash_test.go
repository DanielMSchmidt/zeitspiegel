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
