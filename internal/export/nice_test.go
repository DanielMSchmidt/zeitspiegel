//go:build linux

package export

// UT-16: applyNice lowers a child process's scheduling priority. Linux-only:
// the readback convention below (getpriority(2) returning 20−nice) is
// Linux-specific, and niceness only matters in production on the Pi.

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestApplyNiceLowersChildPriority(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	if err := applyNice(cmd.Process.Pid, 10); err != nil {
		t.Fatalf("applyNice: %v", err)
	}
	// Go's syscall.Getpriority returns the raw kernel value 20−nice
	// (avoiding negative-errno ambiguity), so nice 10 reads back as 10.
	got, err := syscall.Getpriority(syscall.PRIO_PROCESS, cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpriority: %v", err)
	}
	if got != 20-10 {
		t.Fatalf("child priority readback = %d, want %d (nice 10)", got, 20-10)
	}
}

func TestApplyNiceZeroIsNoop(t *testing.T) {
	if err := applyNice(0, 0); err != nil {
		t.Fatalf("applyNice(_, 0) must be a no-op, got %v", err)
	}
}
