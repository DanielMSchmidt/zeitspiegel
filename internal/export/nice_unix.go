//go:build unix

package export

import "syscall"

// applyNice lowers pid's scheduling priority to the given niceness; 0 is a
// no-op (inherit). Raising niceness needs no privileges. Errors are the
// caller's to ignore: the worst case is an export competing at normal
// priority, which was the behavior before niceness existed.
func applyNice(pid, nice int) error {
	if nice == 0 {
		return nil
	}
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}
