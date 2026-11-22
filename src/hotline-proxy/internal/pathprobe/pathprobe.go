// Package pathprobe sends the small packets of a path probe: a TCP or UDP
// packet that may cross only so many routers, or a ping, and says what came
// back. It needs no root and no extra capability: on Linux it reads the
// routers' answers (ICMP, the network's error messages) that the kernel
// keeps for an ordinary socket, and on macOS it reads them from the ICMP
// socket any user may open (docs/decisions/probe.md).
package pathprobe

import (
	"errors"
	"net/netip"
)

// Transport is how a packet goes.
type Transport string

const (
	// TCP is the start of a TCP connection, as a call's own connect.
	TCP Transport = "tcp"
	// UDP is one UDP packet, to a port where nothing should listen.
	UDP Transport = "udp"
	// Ping is one ICMP echo request.
	Ping Transport = "ping"
)

// Packet is one packet to send.
type Packet struct {
	Transport Transport
	To        netip.AddrPort
	// TTL is how many routers the packet may cross before one of them
	// drops it and says so.
	TTL int
}

// Kind is what came back to a packet.
type Kind string

const (
	// Connected is the destination saying yes: a TCP connection opened,
	// a UDP answer or a ping reply.
	Connected Kind = "connected"
	// Reset is the destination saying no to a TCP connection.
	Reset Kind = "reset"
	// RouterError is an ICMP error from a router on the way or from the
	// destination itself, such as "time exceeded" or "port unreachable".
	RouterError Kind = "icmp"
	// Silence is nothing coming back before the wait ended.
	Silence Kind = "silence"
	// NotSent is a packet this machine could not send; Err says why.
	NotSent Kind = "not_sent"
)

// The ICMP messages a path probe reads.
const (
	// ICMPUnreachable is "destination unreachable"; its code says why.
	ICMPUnreachable = 3
	// ICMPTimeExceeded is a router dropping a packet whose TTL ran out.
	ICMPTimeExceeded = 11
)

// Answer is what came back to one packet.
type Answer struct {
	Kind Kind
	// From is who answered: the destination, or the router that sent the
	// ICMP error.
	From netip.Addr
	// ICMPType and ICMPCode are set for a RouterError.
	ICMPType, ICMPCode uint8
	// Err is why a packet was not sent.
	Err error
}

// ErrIPv6 is a packet to an IPv6 address: path probes over IPv6 are not
// built yet.
var ErrIPv6 = errors.New("pathprobe: path probes over IPv6 are not built yet")

// ErrTTL is a TTL outside 1 to 255. macOS takes such a TTL without a word
// and sends the packet anyway, so the check is ours, the same everywhere.
var ErrTTL = errors.New("pathprobe: a TTL must be from 1 to 255")

// Sender sends path probe packets on this machine.
type Sender struct{}

// notSent is the answer to a packet that could not be sent.
func notSent(err error) Answer { return Answer{Kind: NotSent, Err: err} }
