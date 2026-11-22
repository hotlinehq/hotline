//go:build linux || darwin

package pathprobe

import (
	"context"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// pollMillis is how long one wait for an answer lasts before it looks at
// ctx again.
const pollMillis = 10

// ping sends one ICMP echo on the ICMP socket an ordinary user may open:
// always on macOS, and on Linux when the system's ping_group_range lets the
// proxy's group, as Docker sets it. Elsewhere the ping is not sent, and the
// answer says why.
func ping(ctx context.Context, to netip.Addr, ttl int) Answer {
	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err == nil {
		defer func() { _ = c.Close() }()
		err = c.IPv4PacketConn().SetTTL(ttl)
	}
	if err == nil {
		msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: 1, Seq: 1, Data: []byte("hotline probe")}}
		b, _ := msg.Marshal(nil)
		_, err = c.WriteTo(b, &net.UDPAddr{IP: to.AsSlice()})
	}
	if err != nil {
		return notSent(err)
	}
	buf := make([]byte, 1500)
	for ctx.Err() == nil {
		_ = c.SetReadDeadline(time.Now().Add(pollMillis * time.Millisecond))
		n, _, readErr := c.ReadFrom(buf)
		m, parseErr := icmp.ParseMessage(1, buf[:n])
		if readErr == nil && parseErr == nil && m.Type == ipv4.ICMPTypeEchoReply {
			return Answer{Kind: Connected, From: to}
		}
	}
	return Answer{Kind: Silence}
}
