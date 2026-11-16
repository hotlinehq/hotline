package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http/httptrace"
	"net/netip"
	"slices"
	"time"
)

// errProhibited is a vendor address that resolves only to internal IPs.
var errProhibited = errors.New("proxy: the vendor address is an internal IP")

// minShare is the least time one address gets while others are left, the
// same floor Go's own dialer uses.
const minShare = 2 * time.Second

// deadlineKey holds, in a call's context, when the call's vendorTimeout
// runs out by the proxy's clock.
type deadlineKey struct{}

// dialer opens connections to vendors. It looks up the host itself, so the
// call's trace sees the DNS and connect phases whichever resolver is used.
type dialer struct {
	resolver Resolver
	dialer   Dialer
	clock    Clock
	// allowInternal lets it connect to internal IPs; otherwise they are
	// left out, so nobody can use the proxy to reach them.
	allowInternal bool
}

func (d *dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	trace := traceOrNone(ctx)
	host, port, _ := net.SplitHostPort(address)
	ip, err := netip.ParseAddr(host)
	ips := []netip.Addr{ip}
	if err != nil {
		trace.DNSStart(httptrace.DNSStartInfo{Host: host})
		ips, err = d.lookup(ctx, host)
		trace.DNSDone(httptrace.DNSDoneInfo{Err: err})
	}
	if err != nil {
		return nil, err
	}
	if !d.allowInternal {
		ips = slices.DeleteFunc(ips, internal)
		if len(ips) == 0 {
			return nil, errProhibited
		}
	}
	// Addresses are tried one after another, so their connect phases never
	// overlap. Each gets a share of the time left, so one address that never
	// answers can't use up the whole wait.
	var conn net.Conn
	for i := 0; conn == nil && i < len(ips) && ctx.Err() == nil; i++ {
		address = net.JoinHostPort(ips[i].String(), port)
		trace.ConnectStart(network, address)
		conn, err = d.dialOne(ctx, network, address, len(ips)-i)
		trace.ConnectDone(network, address, err)
	}
	return conn, err
}

// traceOrNone is the call's trace hooks, or hooks that do nothing when the
// dial has no call's trace, as with a relay.
func traceOrNone(ctx context.Context) *httptrace.ClientTrace {
	if trace := httptrace.ContextClientTrace(ctx); trace != nil {
		return trace
	}
	return &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) {},
		DNSDone:      func(httptrace.DNSDoneInfo) {},
		ConnectStart: func(string, string) {},
		ConnectDone:  func(string, string, error) {},
	}
}

// lookup asks the resolver for the addresses of host. The real resolver
// opens its own connections to DNS servers, and the call's trace would see
// them as connects to the vendor. So the lookup gets a context without the
// trace, which still ends when the call's context does.
func (d *dialer) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	untraced, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	return d.resolver.LookupNetIP(untraced, "ip", host)
}

// dialOne connects to one address, giving up after its share of the time
// left when other addresses are still to try.
func (d *dialer) dialOne(ctx context.Context, network, address string, left int) (net.Conn, error) {
	deadline, found := ctx.Value(deadlineKey{}).(time.Time)
	if !found {
		// A relay has no vendorTimeout, only its context's own deadline,
		// and with no deadline at all each address may take as long as it
		// needs, as with Go's own dialer.
		deadline, found = ctx.Deadline()
	}
	if left > 1 && found {
		remaining := deadline.Sub(d.clock.Now())
		share := max(remaining/time.Duration(left), min(minShare, remaining))
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		timer := d.clock.AfterFunc(share, cancel)
		defer timer.Stop()
	}
	return d.dialer.DialContext(ctx, network, address)
}

// internal says whether an IP is one the proxy must not reach unless an
// api allows it: loopback, link-local (which holds the cloud metadata
// address), private, shared address space, unspecified and multicast, also
// when an IPv4 address is written in IPv6 form.
func internal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 0 || b[0] == 100 && b[1]&0xc0 == 64 {
			// 0.0.0.0/8 and the shared address space 100.64.0.0/10.
			return true
		}
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

// isCertificateError says whether a TLS handshake failed on the vendor's
// certificate rather than on the protocol.
func isCertificateError(err error) bool {
	var cert *tls.CertificateVerificationError
	return errors.As(err, &cert)
}
