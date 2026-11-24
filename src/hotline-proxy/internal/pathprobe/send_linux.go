//go:build linux

package pathprobe

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"net/netip"

	"golang.org/x/sys/unix"
)

// Send sends one packet and waits for what comes back, until ctx ends. A
// TCP or UDP packet is an ordinary socket with IP_RECVERR on, so the kernel
// keeps the ICMP error a router sends back, with the router's address.
func (Sender) Send(ctx context.Context, p Packet) Answer {
	to := p.To.Addr().Unmap()
	if !to.Is4() {
		return notSent(ErrIPv6)
	}
	if p.Transport == Ping {
		return ping(ctx, to, p.TTL)
	}
	sotype := map[Transport]int{TCP: unix.SOCK_STREAM, UDP: unix.SOCK_DGRAM}[p.Transport]
	fd, err := unix.Socket(unix.AF_INET, sotype|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	defer func() { _ = unix.Close(fd) }()
	pollFd, fdErr := pollable(fd)
	err = errors.Join(err, validTTL(p.TTL), fdErr,
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, p.TTL),
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1))
	if err == nil {
		err = unix.Connect(fd, &unix.SockaddrInet4{Port: int(p.To.Port()), Addr: to.As4()})
	}
	if err == nil && sotype == unix.SOCK_DGRAM {
		_, err = unix.Write(fd, []byte("hotline probe"))
	}
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return notSent(err)
	}
	// A connect waits to be writable; a UDP packet waits for an answer.
	events := map[int]int16{unix.SOCK_STREAM: unix.POLLOUT, unix.SOCK_DGRAM: unix.POLLIN}[sotype]
	fds := []unix.PollFd{{Fd: pollFd, Events: events | unix.POLLERR}}
	for ctx.Err() == nil {
		if n, _ := unix.Poll(fds, pollMillis); n > 0 {
			return answer(fd, to, sotype)
		}
	}
	return Answer{Kind: Silence}
}

// answer reads what came back to the socket: an ICMP error the kernel
// kept, a UDP answer, or how the TCP connect ended.
func answer(fd int, to netip.Addr, sotype int) Answer {
	if a, found := routerError(fd); found {
		return a
	}
	if sotype == unix.SOCK_DGRAM {
		return Answer{Kind: Connected, From: to}
	}
	soErr, _ := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	e := unix.Errno(soErr)
	kind := map[unix.Errno]Kind{0: Connected, unix.ECONNREFUSED: Reset}[e]
	return Answer{Kind: cmp.Or(kind, NotSent), From: to, Err: errorIf(e)}
}

// routerError reads the ICMP error the kernel queued for this socket, with
// the address of the router or host that sent it.
func routerError(fd int) (Answer, bool) {
	oob := make([]byte, 512)
	_, oobn, _, _, err := unix.Recvmsg(fd, make([]byte, 64), oob, unix.MSG_ERRQUEUE)
	msgs, parseErr := unix.ParseSocketControlMessage(oob[:oobn])
	for _, m := range msgs {
		// The kernel's sock_extended_err: errno (4 bytes), origin, type,
		// code, a pad byte, then 8 more bytes. The sender's address
		// follows, a sockaddr_in: its family, its port, then the four bytes
		// of the address.
		if err == nil && parseErr == nil && m.Header.Level == unix.IPPROTO_IP &&
			m.Header.Type == unix.IP_RECVERR && len(m.Data) >= extendedErrSize+8 {
			d := m.Data
			kind := map[uint8]Kind{unix.SO_EE_ORIGIN_ICMP: RouterError}[d[4]]
			return Answer{
				Kind: cmp.Or(kind, NotSent), From: netip.AddrFrom4([4]byte(d[extendedErrSize+4 : extendedErrSize+8])),
				ICMPType: d[5], ICMPCode: d[6], Err: errorIf(unix.Errno(binary.NativeEndian.Uint32(d[0:4]))),
			}, true
		}
	}
	return Answer{}, false
}

// extendedErrSize is the size of the kernel's sock_extended_err.
const extendedErrSize = 16

// errorIf is the errno as an error, or nil for none.
func errorIf(e unix.Errno) error {
	if e == 0 {
		return nil
	}
	return e
}
