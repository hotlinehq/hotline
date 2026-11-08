//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// soOriginalDst is the socket option that gives the address a connection
// was first sent to, before a firewall's REDIRECT changed it.
const soOriginalDst = 80

// originalDestination reads the IPv4 address and port a redirected
// connection was first sent to, from the operating system (Linux only). A
// connection that was not redirected has none, which is an error.
func originalDestination(c net.Conn) (netip.AddrPort, error) {
	tcp, _ := c.(*net.TCPConn) // nil for another kind, which SyscallConn refuses
	var found netip.AddrPort
	var sockErr error
	raw, err := tcp.SyscallConn()
	if err == nil {
		err = raw.Control(func(fd uintptr) {
			// The socket option fills a sockaddr_in: its port and address
			// sit in the bytes where the multicast request keeps its own.
			req, e := syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, soOriginalDst)
			sockErr = e
			if e == nil {
				port := binary.BigEndian.Uint16(req.Multiaddr[2:4])
				found = netip.AddrPortFrom(netip.AddrFrom4([4]byte(req.Multiaddr[4:8])), port)
			}
		})
	}
	return found, errors.Join(err, sockErr)
}
