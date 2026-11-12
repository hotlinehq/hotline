//go:build darwin

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketUser asks macOS which user runs the program on the other end of a
// connection to a socket file: the user it had when it connected, or -1
// when that cannot be told.
func socketUser(c net.Conn) int {
	conn, _ := c.(*net.UnixConn) // nil for another kind, which SyscallConn refuses
	user := -1
	raw, err := conn.SyscallConn()
	if err == nil {
		_ = raw.Control(func(fd uintptr) {
			if cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); err == nil { //nolint:gosec // a file descriptor fits an int
				user = int(cred.Uid) //nolint:gosec // a user number fits an int
			}
		})
	}
	return user
}
