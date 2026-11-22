//go:build darwin

package pathprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// Send sends one packet and waits for what comes back, until ctx ends.
// macOS has no IP_RECVERR, but any user may open an ICMP socket, which sees
// the routers' errors for every packet of this machine. Each error quotes
// the start of the packet it is about, so it is matched to this packet by
// its protocol, its destination and its source port (checked on Peter's
// Mac, 2026-11-21).
func (Sender) Send(ctx context.Context, p Packet) Answer {
	to := p.To.Addr().Unmap()
	if !to.Is4() {
		return notSent(ErrIPv6)
	}
	if p.Transport == Ping {
		return ping(ctx, to, p.TTL)
	}
	errors4, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return notSent(err)
	}
	defer func() { _ = errors4.Close() }()
	sotype := map[Transport]int{TCP: unix.SOCK_STREAM, UDP: unix.SOCK_DGRAM}[p.Transport]
	fd, err := unix.Socket(unix.AF_INET, sotype, 0)
	defer func() { _ = unix.Close(fd) }()
	err = errors.Join(err, validTTL(p.TTL), unix.SetNonblock(fd, true),
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, p.TTL),
		unix.Bind(fd, &unix.SockaddrInet4{}))
	local, nameErr := unix.Getsockname(fd)
	err = errors.Join(err, nameErr)
	if err == nil {
		err = unix.Connect(fd, &unix.SockaddrInet4{Port: int(p.To.Port()), Addr: to.As4()})
	}
	if err == nil && sotype == unix.SOCK_DGRAM {
		_, err = unix.Write(fd, []byte("hotline probe"))
	}
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return notSent(err)
	}
	in4, _ := local.(*unix.SockaddrInet4)
	quote := quoted{protocol: map[int]uint8{unix.SOCK_STREAM: 6, unix.SOCK_DGRAM: 17}[sotype], to: to, port: uint16(in4.Port)} //nolint:gosec // a port fits in 16 bits
	events := map[int]int16{unix.SOCK_STREAM: unix.POLLOUT, unix.SOCK_DGRAM: unix.POLLIN}[sotype]
	fds := []unix.PollFd{{Fd: int32(fd), Events: events}} //nolint:gosec // a file descriptor fits in 32 bits
	buf := make([]byte, 1500)
	for ctx.Err() == nil {
		// A router's error comes first: the socket itself then says only
		// "no route to host".
		_ = errors4.SetReadDeadline(time.Now().Add(pollMillis * time.Millisecond))
		if n, from, readErr := errors4.ReadFrom(buf); readErr == nil {
			if a, found := quote.match(buf[:n], from.String()); found {
				return a
			}
		}
		if n, _ := unix.Poll(fds, 0); n > 0 && fds[0].Revents&(events|unix.POLLHUP) != 0 {
			if a, done := ended(fd, to, sotype); done {
				return a
			}
		}
	}
	return Answer{Kind: Silence}
}

// ended reads how the socket's wait ended: a TCP connect that opened or
// was refused, or a UDP answer. Any other error waits for the router's
// own message on the ICMP socket.
func ended(fd int, to netip.Addr, sotype int) (Answer, bool) {
	if sotype == unix.SOCK_DGRAM {
		_, err := unix.Read(fd, make([]byte, 64))
		return Answer{Kind: Connected, From: to}, err == nil
	}
	soErr, _ := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	switch unix.Errno(soErr) {
	case 0:
		return Answer{Kind: Connected, From: to}, true
	case unix.ECONNREFUSED:
		return Answer{Kind: Reset, From: to, Err: unix.ECONNREFUSED}, true
	}
	return Answer{}, false
}

// quoted is what an ICMP error about this packet quotes of it.
type quoted struct {
	protocol uint8
	to       netip.Addr
	port     uint16
}

// match reads an ICMP message from the ICMP socket, and says whether it is
// an error about this packet.
func (q quoted) match(b []byte, from string) (Answer, bool) {
	m, err := icmp.ParseMessage(1, b)
	if err != nil {
		return Answer{}, false
	}
	var original []byte
	switch body := m.Body.(type) {
	case *icmp.TimeExceeded:
		original = body.Data
	case *icmp.DstUnreach:
		original = body.Data
	default:
		return Answer{}, false
	}
	// The original packet: its IP header, then its first 8 bytes, where a
	// TCP or UDP header starts with the source port.
	if len(original) < 20 {
		return Answer{}, false
	}
	header := int(original[0]&0x0f) * 4
	if len(original) < header+2 || original[9] != q.protocol ||
		netip.AddrFrom4([4]byte(original[16:20])) != q.to ||
		binary.BigEndian.Uint16(original[header:header+2]) != q.port {
		return Answer{}, false
	}
	// The ICMP socket names the sender as an address with port 0.
	sender, _ := netip.ParseAddrPort(from)
	typ := uint8(m.Type.(ipv4.ICMPType))                                                                        //nolint:forcetypeassert,gosec // ParseMessage of protocol 1 gives ipv4 types, each a byte
	return Answer{Kind: RouterError, From: sender.Addr().Unmap(), ICMPType: typ, ICMPCode: uint8(m.Code)}, true //nolint:gosec // an ICMP code is a byte
}
