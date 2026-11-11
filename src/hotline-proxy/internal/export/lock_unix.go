//go:build unix

package export

import (
	"os"
	"syscall"
)

// lockExclusive takes the file's lock without waiting. The system lets go of
// it when the proxy ends, even when it dies.
func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // a file descriptor fits an int
}
