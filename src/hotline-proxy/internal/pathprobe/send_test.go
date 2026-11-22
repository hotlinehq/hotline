//go:build linux || darwin

package pathprobe_test

import (
	"context"
	"net"
	"net/netip"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/pathprobe"
)

// These send real packets on this machine's loopback, as an ordinary
// socket does, so they need no root. Routers further away cannot be
// faked here: the proxy's own tests use a fake network for those.
var _ = Describe("Sending a path probe packet", func() {
	loopback := netip.MustParseAddr("127.0.0.1")
	send := func(t pathprobe.Transport, port, ttl int) pathprobe.Answer {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return pathprobe.Sender{}.Send(ctx, pathprobe.Packet{
			Transport: t, To: netip.AddrPortFrom(loopback, uint16(port)), TTL: ttl,
		})
	}
	freePort := func(network string) int {
		c, err := net.ListenPacket(network, "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		port := c.LocalAddr().(*net.UDPAddr).Port
		Expect(c.Close()).To(Succeed())
		return port
	}

	It("says a listening TCP port connected", func() {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = l.Close() }()
		a := send(pathprobe.TCP, l.Addr().(*net.TCPAddr).Port, 64)
		Expect(a.Kind).To(Equal(pathprobe.Connected))
		Expect(a.From).To(Equal(loopback))
	})

	It("says a closed TCP port reset the connection", func() {
		a := send(pathprobe.TCP, freePort("udp4"), 64)
		Expect(a.Kind).To(Equal(pathprobe.Reset))
		Expect(a.From).To(Equal(loopback))
	})

	It("reads the ICMP port unreachable a closed UDP port sends back, and who sent it", func() {
		a := send(pathprobe.UDP, freePort("udp4"), 64)
		Expect(a.Kind).To(Equal(pathprobe.RouterError))
		Expect(a.From).To(Equal(loopback))
		Expect(a.ICMPType).To(BeEquivalentTo(3))
		Expect(a.ICMPCode).To(BeEquivalentTo(3))
	})

	It("says a UDP port that answers connected", func() {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = c.Close() }()
		go func() {
			buf := make([]byte, 64)
			n, from, err := c.ReadFrom(buf)
			if err == nil {
				_, _ = c.WriteTo(buf[:n], from)
			}
		}()
		a := send(pathprobe.UDP, c.LocalAddr().(*net.UDPAddr).Port, 64)
		Expect(a.Kind).To(Equal(pathprobe.Connected))
	})

	It("hears silence when the wait has ended", func() {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = c.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, t := range []pathprobe.Transport{pathprobe.UDP, pathprobe.Ping} {
			a := pathprobe.Sender{}.Send(ctx, pathprobe.Packet{
				Transport: t, To: netip.AddrPortFrom(loopback, uint16(c.LocalAddr().(*net.UDPAddr).Port)), TTL: 64,
			})
			Expect(a.Kind).To(Equal(pathprobe.Silence), string(t))
		}
	})

	It("says why a packet with no routers to cross is not sent", func() {
		for _, t := range []pathprobe.Transport{pathprobe.TCP, pathprobe.Ping} {
			a := send(t, 9, 0)
			Expect(a.Kind).To(Equal(pathprobe.NotSent), string(t))
			Expect(a.Err).To(HaveOccurred())
		}
	})

	It("sends nothing over IPv6 yet", func() {
		a := pathprobe.Sender{}.Send(context.Background(), pathprobe.Packet{
			Transport: pathprobe.TCP, To: netip.MustParseAddrPort("[::1]:9"), TTL: 64,
		})
		Expect(a.Kind).To(Equal(pathprobe.NotSent))
		Expect(a.Err).To(MatchError(pathprobe.ErrIPv6))
	})

	It("gets the reply to a ping, where the system lets the proxy's group ping", func() {
		a := send(pathprobe.Ping, 0, 64)
		Expect(a.Kind).To(Equal(pathprobe.Connected))
		Expect(a.From).To(Equal(loopback))
	})
})
