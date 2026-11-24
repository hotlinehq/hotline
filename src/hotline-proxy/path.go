package proxy

import (
	"context"
	"hotline-proxy/internal/fits"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/pathprobe"
	"hotline-proxy/internal/tracectx"
)

// The path trace of a probe (slice 2, Peter, 2026-11-22). It runs only when
// the vendor's port gave no answer or could not be reached: a refusal or a
// TLS failure already says which side the problem is on. It sends packets
// that may cross only 1 router, then 2, and so on, over TCP to the call's
// port and over UDP, and reads which router answers each. Then it asks
// whether the vendor's machine is up: the UDP trace's last packet, to a
// port where nothing listens, gets "port unreachable" from a machine that
// is up; when it does not, a ping, where the system allows one.

// silentHopsToStop is how many routers in a row may stay silent before
// the trace gives up, as traceroute does.
const silentHopsToStop = 3

// udpFirstPort is the first UDP port the trace sends to, one more for each
// router, as traceroute does: ports where nothing should listen.
const udpFirstPort = 33434

// pingTTL lets a ping cross every router on the way.
const pingTTL = 64

// PacketSender sends the packets of a path probe and says what came back.
// pathprobe.Sender is the real one.
type PacketSender interface {
	Send(ctx context.Context, p pathprobe.Packet) pathprobe.Answer
}

// hop is one packet of the path trace and what came back to it.
type hop struct {
	packet     pathprobe.Packet
	answer     pathprobe.Answer
	start, end time.Time
	// timeout is how long it waited for an answer.
	timeout time.Duration
}

// tracePath answers path_reaches_vendor and host_up, and gives the verdict
// that replaces the connect's own: where the packets stop, and who turned
// them away. Where this machine cannot send path packets, both questions
// stay not checked and the connect's verdict stands.
func (p *probes) tracePath(ctx context.Context, ip netip.Addr, port string, settings config.Probe, f *probeFound, verdict string) string {
	path, host, accepts := &f.questions[1], &f.questions[2], &f.questions[3]
	// The port comes from an address the proxy already dialled. Text that is
	// not a port reads as 0, or as 65535 when it is too big, as before.
	vendorPort, _ := strconv.ParseUint(port, 10, 16)
	path.start = p.clock.Now()
	tcp := p.trace(ctx, pathprobe.TCP, ip, uint16(vendorPort), settings)
	if tcp[0].answer.Kind == pathprobe.NotSent {
		path.evidence = []string{"not checked: this machine cannot send path packets: " + tcp[0].answer.Err.Error()}
		host.evidence = path.evidence
		path.start = time.Time{}
		return verdict
	}
	udp := p.trace(ctx, pathprobe.UDP, ip, udpFirstPort, settings)
	path.end = p.clock.Now()
	path.hops = slices.Concat(tcp, udp)
	path.evidence = hopLines(path.hops)

	host.start = path.end
	reached := slices.ContainsFunc(udp, func(h hop) bool { return h.answer.From == ip })
	host.evidence = []string{"udp: " + answeredBy(reached, ip)}
	if !reached {
		ping := p.send(ctx, pathprobe.Packet{Transport: pathprobe.Ping, To: netip.AddrPortFrom(ip, 0), TTL: pingTTL}, settings.Timeout)
		reached = ping.answer.Kind == pathprobe.Connected
		host.evidence = append(host.evidence, hopLine(ping))
	}
	host.end = p.clock.Now()
	host.answer = answerOf(reached)

	// The port's "no" now points to the vendor: the path questions before
	// it tell whether the packets got there.
	accepts.pointsTo = sideVendor
	last, lastFound := lastAnswered(path.hops)
	path.answer = answerOf(reached)
	if !reached && lastFound && internal(last.answer.From) {
		path.pointsTo = sideOurs
	}
	firewall, prohibited := firstWith(path.hops, prohibitedAnswer)
	// Before the vendor answered, a "destination unreachable" came from a
	// router on the way.
	unreachable, unreachableFound := firstWith(path.hops, func(a pathprobe.Answer) bool { return a.ICMPType == pathprobe.ICMPUnreachable })
	switch {
	case prohibited:
		f.where = firewall.answer.From.String()
		return "rejected_by_firewall"
	case reached:
		return "silent_drop_at_vendor"
	case path.pointsTo == sideOurs:
		f.where = routerName(last)
		return "path_stops_on_our_side"
	case unreachableFound:
		f.where = unreachable.answer.From.String()
		return "unreachable"
	case lastFound:
		f.where = "after " + routerName(last)
		return "silent_drop"
	}
	f.where = "from the first router"
	return "silent_drop"
}

// trace sends packets to ip with a growing TTL, until the vendor's machine
// or a router turning the packets away answers, the packets cannot be
// sent, maxHops routers have been asked, or three in a row stay silent.
func (p *probes) trace(ctx context.Context, transport pathprobe.Transport, ip netip.Addr, port uint16, settings config.Probe) []hop {
	var hops []hop
	silent := 0
	last := settings.MaxHops
	if transport == pathprobe.UDP {
		// Each UDP packet goes to the next port, so the trace ends at the
		// last port rather than wrapping round to port 0.
		last = min(last, math.MaxUint16-int(port)+1)
	}
	for ttl := 1; ttl <= last && silent < silentHopsToStop; ttl++ {
		to := int(port)
		if transport == pathprobe.UDP {
			to += ttl - 1
		}
		toPort, _ := fits.Uint16(to) // always fits: port does, and the loop ends at the last port
		h := p.send(ctx, pathprobe.Packet{Transport: transport, To: netip.AddrPortFrom(ip, toPort), TTL: ttl}, settings.Timeout)
		hops = append(hops, h)
		a := h.answer
		if a.From == ip || a.Kind != pathprobe.Silence && a.ICMPType != pathprobe.ICMPTimeExceeded {
			break
		}
		if a.Kind == pathprobe.Silence {
			silent++
		} else {
			silent = 0
		}
	}
	return hops
}

// send sends one packet, waiting at most timeout by the proxy's clock.
func (p *probes) send(ctx context.Context, packet pathprobe.Packet, timeout time.Duration) hop {
	ctx, _, done := p.stepContext(ctx, timeout)
	defer done()
	start := p.clock.Now()
	a := p.packets.Send(ctx, packet)
	return hop{packet: packet, answer: a, start: start, end: p.clock.Now(), timeout: timeout}
}

// prohibitedAnswer is a firewall saying no and naming itself: ICMP
// "destination unreachable", administratively prohibited (codes 9, 10 and
// 13).
func prohibitedAnswer(a pathprobe.Answer) bool {
	return a.ICMPType == pathprobe.ICMPUnreachable && (a.ICMPCode == 9 || a.ICMPCode == 10 || a.ICMPCode == 13)
}

// firstWith is the first hop whose answer matches.
func firstWith(hops []hop, match func(pathprobe.Answer) bool) (hop, bool) {
	i := slices.IndexFunc(hops, func(h hop) bool { return match(h.answer) })
	if i < 0 {
		return hop{}, false
	}
	return hops[i], true
}

// lastAnswered is the hop furthest along the path that got an answer from
// someone, in either trace.
func lastAnswered(hops []hop) (hop, bool) {
	var last hop
	for _, h := range hops {
		if h.answer.From.IsValid() && h.packet.TTL > last.packet.TTL {
			last = h
		}
	}
	return last, last.answer.From.IsValid()
}

// routerName names a router of the path, as "router 2 (198.51.100.2)".
func routerName(h hop) string {
	return "router " + strconv.Itoa(h.packet.TTL) + " (" + h.answer.From.String() + ")"
}

// answerOf is yes or no.
func answerOf(yes bool) string {
	if yes {
		return answerYes
	}
	return answerNo
}

// answeredBy says whether the vendor's machine answered the UDP trace.
func answeredBy(answered bool, ip netip.Addr) string {
	if answered {
		return ip.String() + " answered"
	}
	return "no answer from " + ip.String()
}

// hopWord is what came back to a packet, in one word.
func hopWord(a pathprobe.Answer) string {
	switch {
	case a.Kind != pathprobe.RouterError:
		return string(a.Kind)
	case a.ICMPType == pathprobe.ICMPTimeExceeded:
		return "time_exceeded"
	case prohibitedAnswer(a):
		return "prohibited"
	case a.ICMPCode == 3:
		return "port_unreachable"
	}
	return "unreachable"
}

// hopLines are the trace's packets as evidence, one line each.
func hopLines(hops []hop) []string {
	lines := make([]string, len(hops))
	for i, h := range hops {
		lines[i] = hopLine(h)
	}
	return lines
}

// hopLine is one packet as a line of evidence, such as "tcp ttl 2: time
// exceeded from 198.51.100.2".
func hopLine(h hop) string {
	line := string(h.packet.Transport)
	if h.packet.Transport != pathprobe.Ping {
		line += " ttl " + strconv.Itoa(h.packet.TTL)
	}
	line += ": "
	switch h.answer.Kind {
	case pathprobe.Silence:
		return line + "no answer within " + h.timeout.String()
	case pathprobe.NotSent:
		return line + "not sent: " + h.answer.Err.Error()
	}
	return line + strings.ReplaceAll(hopWord(h.answer), "_", " ") + " from " + h.answer.From.String()
}

// span is the hop as a span under the path question's span.
func (h hop) span(parent trace.SpanID) export.Span {
	attrs := []attribute.KeyValue{
		attribute.Int("hotline.probe.hop.ttl", h.packet.TTL),
		attribute.String("hotline.probe.hop.answer", hopWord(h.answer)),
		semconv.NetworkTransportKey.String(string(h.packet.Transport)),
	}
	if h.answer.From.IsValid() {
		attrs = append(attrs, attribute.String("hotline.probe.hop.from", h.answer.From.String()))
	}
	if h.answer.Kind == pathprobe.RouterError {
		attrs = append(attrs,
			attribute.Int("hotline.probe.hop.icmp_type", int(h.answer.ICMPType)),
			attribute.Int("hotline.probe.hop.icmp_code", int(h.answer.ICMPCode)))
	}
	return export.Span{
		ID: tracectx.NewSpanID(), Parent: parent, Name: "hop", Kind: trace.SpanKindInternal,
		Start: h.start, End: h.end, Attrs: attrs,
	}
}
