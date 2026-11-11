//go:build e2e && linux

package proxy_test

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openTerminal makes a terminal so many columns wide for the program to
// print to: a pseudo terminal, whose other end the test reads.
func openTerminal(columns int) (*terminal, error) {
	reader, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	fd := int(reader.Fd()) //nolint:gosec // a file descriptor fits an int
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = reader.Close()
		return nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	program, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	size := &unix.Winsize{Row: 24, Col: uint16(columns)}                                   //nolint:gosec // a small width
	if err := unix.IoctlSetWinsize(int(program.Fd()), unix.TIOCSWINSZ, size); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = reader.Close()
		_ = program.Close()
		return nil, err
	}
	t := &terminal{program: program, reader: reader, done: make(chan struct{})}
	go func() {
		defer close(t.done)
		// The read ends with an error once the program's end is closed.
		_, _ = io.Copy(&t.shown, reader)
	}()
	return t, nil
}
