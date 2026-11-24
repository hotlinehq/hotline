//go:build unix

package main

import (
	"cmp"
	"os"

	"golang.org/x/sys/unix"
)

// terminal gives the width of the terminal f is, and false when f is not
// a terminal: then the size cannot be read.
func terminal(f *os.File) func() (int, bool) {
	return func() (int, bool) {
		size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
		return int(cmp.Or(size, &unix.Winsize{}).Col), err == nil
	}
}
