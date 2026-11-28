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
	sut := probeSendSUT{}

	It("says a listening TCP port connected", func() {
		sut.forListeningTCPPort()
		a := sut.Send(pathprobe.TCP)
		Expect(a.Kind).To(Equal(pathprobe.Connected))
		Expect(a.From).To(Equal(sut.Loopback()))
	})

	It("says a closed TCP port reset the connection", func() {
		sut.forClosedPort()
		a := sut.Send(pathprobe.TCP)
		Expect(a.Kind).To(Equal(pathprobe.Reset))
		Expect(a.From).To(Equal(sut.Loopback()))
	})

	It("reads the ICMP port unreachable a closed UDP port sends back, and who sent it", func() {
		sut.forClosedPort()
		a := sut.Send(pathprobe.UDP)
		Expect(a.Kind).To(Equal(pathprobe.RouterError))
		Expect(a.From).To(Equal(sut.Loopback()))
		Expect(a.ICMPType).To(BeEquivalentTo(3))
		Expect(a.ICMPCode).To(BeEquivalentTo(3))
	})

	It("says a UDP port that answers connected", func() {
		sut.forUDPPortThatAnswers()
		Expect(sut.Send(pathprobe.UDP).Kind).To(Equal(pathprobe.Connected))
	})

	It("hears silence when the wait has ended", func() {
		sut.forUDPPortThatNeverAnswers()
		for _, t := range []pathprobe.Transport{pathprobe.UDP, pathprobe.Ping} {
			Expect(sut.SendWithTheWaitOver(t).Kind).To(Equal(pathprobe.Silence), string(t))
		}
	})

	It("keeps waiting while nothing comes back, and hears silence when the wait ends", func() {
		// A UDP port that takes the packet and never answers: the wait
		// polls more than once before it ends, every time, not only when
		// the machine happens to be slow (docs/flakes.md).
		sut.forUDPPortThatNeverAnswers()
		Expect(sut.SendWaiting(pathprobe.UDP, 100*time.Millisecond).Kind).To(Equal(pathprobe.Silence))
	})

	It("says why a packet with no routers to cross is not sent", func() {
		sut.forPort(9)
		for _, t := range []pathprobe.Transport{pathprobe.TCP, pathprobe.Ping} {
			a := sut.SendCrossingRouters(t, 0)
			Expect(a.Kind).To(Equal(pathprobe.NotSent), string(t))
			Expect(a.Err).To(MatchError(pathprobe.ErrTTL), string(t))
		}
	})

	It("sends nothing over IPv6 yet", func() {
		sut.forIPv6Port(9)
		a := sut.Send(pathprobe.TCP)
		Expect(a.Kind).To(Equal(pathprobe.NotSent))
		Expect(a.Err).To(MatchError(pathprobe.ErrIPv6))
	})

	It("gets the reply to a ping, where the system lets the proxy's group ping", func() {
		sut.forPort(0)
		a := sut.Send(pathprobe.Ping)
		Expect(a.Kind).To(Equal(pathprobe.Connected))
		Expect(a.From).To(Equal(sut.Loopback()))
	})
})

// probeSendSUT is one address on this machine's loopback that probes are
// sent to, and what listens there.
type probeSendSUT struct{ to netip.AddrPort }

// forPort is port on the loopback, with nothing set up there.
func (s *probeSendSUT) forPort(port uint16) { s.to = netip.AddrPortFrom(s.Loopback(), port) }

// forIPv6Port is port on the IPv6 loopback.
func (s *probeSendSUT) forIPv6Port(port uint16) {
	s.to = netip.AddrPortFrom(netip.MustParseAddr("::1"), port)
}

// forListeningTCPPort is a TCP port that takes connections.
func (s *probeSendSUT) forListeningTCPPort() {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(l.Close)
	s.to = l.Addr().(*net.TCPAddr).AddrPort()
}

// forClosedPort is a port nothing listens on.
func (s *probeSendSUT) forClosedPort() {
	c, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.to = c.LocalAddr().(*net.UDPAddr).AddrPort()
	Expect(c.Close()).WithOffset(1).To(Succeed())
}

// forUDPPortThatNeverAnswers is a UDP port that takes packets and never
// answers.
func (s *probeSendSUT) forUDPPortThatNeverAnswers() { s.listenUDP() }

// forUDPPortThatAnswers is a UDP port that sends the first packet back.
func (s *probeSendSUT) forUDPPortThatAnswers() {
	c := s.listenUDP()
	go func() {
		buf := make([]byte, 64)
		n, from, err := c.ReadFrom(buf)
		if err == nil {
			_, _ = c.WriteTo(buf[:n], from)
		}
	}()
}

func (s *probeSendSUT) listenUDP() net.PacketConn {
	c, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	DeferCleanup(c.Close)
	s.to = c.LocalAddr().(*net.UDPAddr).AddrPort()
	return c
}

// Loopback is the address the probes go to.
func (*probeSendSUT) Loopback() netip.Addr { return netip.MustParseAddr("127.0.0.1") }

// Send sends a probe that may cross 64 routers, waiting up to 2 seconds
// for what comes back.
func (s *probeSendSUT) Send(t pathprobe.Transport) pathprobe.Answer {
	return s.SendCrossingRouters(t, 64)
}

// SendCrossingRouters sends a probe that may cross ttl routers.
func (s *probeSendSUT) SendCrossingRouters(t pathprobe.Transport, ttl int) pathprobe.Answer {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.send(ctx, t, ttl)
}

// SendWaiting sends a probe, waiting up to wait for what comes back.
func (s *probeSendSUT) SendWaiting(t pathprobe.Transport, wait time.Duration) pathprobe.Answer {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return s.send(ctx, t, 64)
}

// SendWithTheWaitOver sends a probe whose wait has already ended.
func (s *probeSendSUT) SendWithTheWaitOver(t pathprobe.Transport) pathprobe.Answer {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return s.send(ctx, t, 64)
}

func (s *probeSendSUT) send(ctx context.Context, t pathprobe.Transport, ttl int) pathprobe.Answer {
	return pathprobe.Sender{}.Send(ctx, pathprobe.Packet{Transport: t, To: s.to, TTL: ttl})
}
