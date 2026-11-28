package proxy_test

import (
	"crypto/tls"
	proxy "hotline-proxy"
	"net"
	"net/http/httptest"
	"time"
)

// vendorsSUT is the world outside the proxy: the fake vendors, the fake
// network and DNS they are reached over, and the certificate authority
// their certificates come from.
type vendorsSUT struct {
	proxy     *proxySUT
	authority *authority
	network   *network
	vendors   map[string]*vendor
	// talker is a vendor that speaks raw bytes over TLS, for relayed
	// tunnels.
	talker *talker
	// missing are hosts the fake DNS does not know.
	missing map[string]bool
	// quiet, when closed, lets a quiet vendor send the rest of its
	// response, and cutOff is closed when the proxy stops waiting for it.
	quiet  chan struct{}
	cutOff chan struct{}
}

func newVendorsSUT(root *proxySUT) *vendorsSUT {
	sut := &vendorsSUT{
		proxy:     root,
		authority: newAuthority(),
		network:   newNetwork(),
		vendors:   map[string]*vendor{},
		missing:   map[string]bool{},
	}
	sut.network.stuck = sut.stuck
	root.addOption(func(o *proxy.Options) {
		o.Resolver, o.Dialer, o.Packets = sut.network, sut.network, sut.network
		o.RootCAs = sut.authority.pool
	})
	return sut
}

// Close stops the vendors.
func (sut *vendorsSUT) Close() {
	for _, v := range sut.vendors {
		v.close()
	}
}

// vendor returns the fake vendor for host, starting it on first use.
func (sut *vendorsSUT) vendor(host string) *vendor {
	if v, ok := sut.vendors[host]; ok {
		return v
	}
	v := &vendor{host: host, ip: ipFor(len(sut.vendors) + 10)}
	sut.vendors[host] = v
	sut.network.name(host, v.ip)
	v.notAfter = time.Now().Add(24 * time.Hour).Truncate(time.Second)
	cert := sut.authority.leaf(host, v.notAfter, false)
	for _, port := range []string{"443", "8443", "8080"} {
		s := httptest.NewUnstartedServer(v.record("https", port))
		s.EnableHTTP2 = true
		s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		s.StartTLS()
		v.servers = append(v.servers, s)
		sut.network.route(net.JoinHostPort(v.ip.String(), port), s.Listener.Addr().String())
	}
	// Port 80 answers plain http and, since the proxy keeps the app's port
	// when it turns a call into https, also https.
	port80 := sharedPort()
	plain := httptest.NewUnstartedServer(v.record("http", "80"))
	plain.Listener = port80.plain
	plain.Start()
	secure := httptest.NewUnstartedServer(v.record("https", "80"))
	secure.Listener = port80.tls
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	v.servers = append(v.servers, plain, secure)
	sut.network.route(net.JoinHostPort(v.ip.String(), "80"), port80.addr)
	return v
}

// replaceTLS swaps the certificate a vendor shows on https port 443.
func (sut *vendorsSUT) replaceTLS(host string, cert tls.Certificate) {
	v := sut.vendor(host)
	s := httptest.NewUnstartedServer(v.record("https", "443"))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	s.StartTLS()
	v.servers = append(v.servers, s)
	sut.network.route(net.JoinHostPort(v.ip.String(), "443"), s.Listener.Addr().String())
}

// route sends an address of the fake network to a local listener.
func (sut *vendorsSUT) routeTo(host, port string, listener net.Listener) {
	v := sut.vendor(host)
	sut.network.route(net.JoinHostPort(v.ip.String(), port), listener.Addr().String())
}

// refuse makes the vendor at host refuse connections on port: they go to a
// reserved port where nothing listens, which no other server can be given.
func (sut *vendorsSUT) refuse(host, port string) {
	v := sut.vendor(host)
	sut.network.route(net.JoinHostPort(v.ip.String(), port), sut.proxy.ports.take())
}

// stuck lets an hour pass when the proxy waits on a fake that never
// answers, so any vendorTimeout runs out.
func (sut *vendorsSUT) stuck() {
	if sut.proxy.clock != nil {
		sut.proxy.clock.pass(time.Hour)
	}
}

// waitForVendorCalls waits until the vendor for host got n calls.
func (sut *vendorsSUT) waitForVendorCalls(host string, n int) error {
	if !eventually(waitLimit, func() bool { return len(sut.vendor(host).received()) >= n }) {
		return failf("the vendor for %s did not get %d calls", host, n)
	}
	return nil
}
