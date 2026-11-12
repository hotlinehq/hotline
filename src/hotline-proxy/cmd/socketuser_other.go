//go:build !linux && !darwin

package main

import "net"

// socketUser is not given on other systems, so listen.capture is refused
// there.
var socketUser func(net.Conn) int
