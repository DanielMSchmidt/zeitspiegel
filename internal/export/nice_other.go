//go:build !unix

package export

// applyNice is a no-op where setpriority(2) does not exist.
func applyNice(pid, nice int) error { return nil }
