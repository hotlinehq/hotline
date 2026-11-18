//go:build unix

package fuzzlog

import (
	"os"

	"golang.org/x/sys/unix"
)

// redirectStderr makes f the process's standard error, also for what the
// Go runtime writes there when it crashes.
func redirectStderr(f *os.File) error {
	return unix.Dup2(int(f.Fd()), int(os.Stderr.Fd())) //nolint:gosec // file descriptors fit an int
}
