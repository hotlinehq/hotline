//go:build !linux

package main

import (
	"errors"
	"net"
	"net/netip"
)

// originalDestination cannot be read on this system: only Linux says where
// a redirected connection was first sent.
func originalDestination(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("the original destination can be read only on Linux")
}
