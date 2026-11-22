package proxy_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"

	"hotline-proxy/internal/pathprobe"
)

var errUnreachable = errors.New("fake network: no route to host")

// certificate authority is a test certificate authority: the one the proxy trusts.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newAuthority() *authority {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hotline test certificate authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &authority{cert: cert, key: key, pool: pool}
}

// leaf makes a certificate for host, for a server or a client. With signer nil it signs
// itself, so no certificate authority vouches for it.
func (i *authority) leaf(host string, notAfter time.Time, selfSigned bool) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	parent, signer := i.cert, i.key
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// vendorCall is what a fake vendor received.
type vendorCall struct {
	scheme string
	port   string
	proto  string
	method string
	uri    string
	host   string
	header http.Header
	body   []byte
	// contentLength is -1 when the body came in chunks of unknown size.
	contentLength int64
}

// vendor is a fake vendor: one host that answers on https ports 443, 8443
// and 8080, and on port 80 both http and https, of the fake network.
type vendor struct {
	host     string
	notAfter time.Time
	ip       netip.Addr
	servers  []*httptest.Server
	mu       sync.Mutex
	calls    []vendorCall
	answer   http.HandlerFunc
}

func (v *vendor) record(scheme, port string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := vendorCall{
			scheme: scheme, port: port, proto: r.Proto, method: r.Method,
			uri: r.RequestURI, host: r.Host, header: r.Header.Clone(), contentLength: r.ContentLength,
		}
		v.mu.Lock()
		answer := v.answer
		v.mu.Unlock()
		if answer == nil {
			c.body, _ = io.ReadAll(r.Body)
		}
		v.mu.Lock()
		v.calls = append(v.calls, c)
		v.mu.Unlock()
		if answer != nil {
			answer(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req-5521")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

// forget drops the calls the vendor kept, so a long fuzz run does not
// fill the memory.
func (v *vendor) forget() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = nil
}

func (v *vendor) received() []vendorCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]vendorCall(nil), v.calls...)
}

func (v *vendor) setAnswer(h http.HandlerFunc) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.answer = h
}

func (v *vendor) close() {
	for _, s := range v.servers {
		s.CloseClientConnections()
		s.Close()
	}
}

// shared is one local port that hands each connection to a plain or a TLS
// listener, by the first byte the caller sends: 0x16 starts a TLS handshake.
type shared struct {
	addr       string
	plain, tls *handedListener
}

func sharedPort() shared {
	l, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	var once sync.Once
	closeAll := func() { once.Do(func() { _ = l.Close() }) }
	p := shared{
		addr:  l.Addr().String(),
		plain: &handedListener{conns: make(chan net.Conn), addr: l.Addr(), done: make(chan struct{}), closeAll: closeAll},
		tls:   &handedListener{conns: make(chan net.Conn), addr: l.Addr(), done: make(chan struct{}), closeAll: closeAll},
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				first, err := r.Peek(1)
				if err != nil {
					_ = c.Close()
					return
				}
				to := p.plain
				if first[0] == 0x16 {
					to = p.tls
				}
				to.hand(&peekedConn{Conn: c, r: r})
			}()
		}
	}()
	return p
}

// handedListener is a listener whose connections come from a shared port.
type handedListener struct {
	conns    chan net.Conn
	addr     net.Addr
	done     chan struct{}
	once     sync.Once
	closeAll func()
}

func (l *handedListener) hand(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		_ = c.Close()
	}
}

func (l *handedListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *handedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	l.closeAll()
	return nil
}

func (l *handedListener) Addr() net.Addr { return l.addr }

// peekedConn reads the byte the shared port peeked at before the rest.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// network is the fake network between the proxy and the vendors: its own
// DNS, and addresses that lead to local test servers.
type network struct {
	mu     sync.Mutex
	names  map[string][]netip.Addr
	routes map[string]string
	// hang lists addresses or names that never answer.
	hang map[string]bool
	// unreachable lists addresses with no route.
	unreachable map[string]bool
	// refusalsLeft is how many more connections to each address are refused, as
	// a vendor whose server is down for a moment refuses them.
	refusalsLeft map[string]int
	// namesAfterLookup are the addresses a name resolves to after its next lookup,
	// as when a vendor's DNS changes.
	namesAfterLookup map[string][]netip.Addr
	lookups          int
	dials            int
	// dialed are the addresses the proxy tried to connect to.
	dialed []string
	// lookupTime is how long each answered lookup takes.
	lookupTime time.Duration
	// stuck runs when a lookup or a connection hangs.
	stuck func()
	// paths are the networks between the proxy and each vendor IP, for
	// the path probes. A vendor IP with none is one this machine cannot
	// send path packets to.
	paths map[netip.Addr]*path
	// packets are the path packets the proxy sent.
	packets []pathprobe.Packet
}

// path is the network between the proxy and one vendor IP.
type path struct {
	// routers are the routers on the way, in order. An invalid address is
	// a router that stays silent.
	routers []netip.Addr
	// diesAfter is how many routers packets cross before something drops
	// them; zero when they reach the vendor's machine.
	diesAfter int
	// prohibitedBy and unreachableBy are the router, counted from 1, that
	// answers "administratively prohibited" or "host unreachable"; zero
	// for none.
	prohibitedBy, unreachableBy int
	// answersUDP and answersPing say what the vendor's machine answers. It
	// never answers TCP: these are vendors whose firewall drops us.
	answersUDP, answersPing bool
	// pingNotAllowed is a system that does not let the proxy ping.
	pingNotAllowed bool
}

func newNetwork() *network {
	return &network{
		names:            map[string][]netip.Addr{},
		routes:           map[string]string{},
		hang:             map[string]bool{},
		unreachable:      map[string]bool{},
		refusalsLeft:     map[string]int{},
		namesAfterLookup: map[string][]netip.Addr{},
		paths:            map[netip.Addr]*path{},
	}
}

func (n *network) LookupNetIP(ctx context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	n.lookups++
	ips, found := n.names[host]
	if next, changes := n.namesAfterLookup[host]; changes {
		n.names[host] = next
		delete(n.namesAfterLookup, host)
	}
	hang := n.hang[host]
	lookupTime := n.lookupTime
	n.mu.Unlock()
	time.Sleep(lookupTime)
	if hang {
		n.stuck()
		<-ctx.Done()
		return nil, &net.DNSError{Err: "timeout", Name: host, IsTimeout: true}
	}
	if !found {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return ips, nil
}

func (n *network) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	n.mu.Lock()
	n.dials++
	n.dialed = append(n.dialed, address)
	local, found := n.routes[address]
	hang, unreachable := n.hang[address], n.unreachable[address]
	refused := n.refusalsLeft[address] > 0
	if refused {
		n.refusalsLeft[address]--
	}
	n.mu.Unlock()
	switch {
	case refused:
		return nil, &net.OpError{Op: "dial", Net: network, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	case hang:
		n.stuck()
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
	case unreachable || !found:
		return nil, &net.OpError{Op: "dial", Net: network, Err: errUnreachable}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, local)
	if err != nil {
		return nil, err
	}
	remote, _ := net.ResolveTCPAddr("tcp", address)
	return &fakeConn{Conn: conn, remote: remote}, nil
}

func (n *network) route(address, local string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.routes[address] = local
}

func (n *network) name(host string, ip netip.Addr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.names[host] = []netip.Addr{ip}
}

func (n *network) setHang(target string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hang[target] = true
}

func (n *network) setUnreachable(address string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.unreachable[address] = true
}

// refuseNext refuses the next connection to address.
func (n *network) refuseNext(address string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.refusalsLeft[address]++
}

func (n *network) counts() (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lookups, n.dials
}

// fakeConn reports the address the proxy asked for, not the local test
// server it really reached.
type fakeConn struct {
	net.Conn
	remote net.Addr
}

func (c *fakeConn) RemoteAddr() net.Addr { return c.remote }

// CloseWrite stops sending and keeps reading, as a real TCP connection to
// a vendor can.
func (c *fakeConn) CloseWrite() error { return c.Conn.(closeWriter).CloseWrite() }

func listenLocal() net.Listener {
	l, _ := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	return l
}

func ipFor(n int) netip.Addr {
	return netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", n))
}

// resolve makes host resolve to ips. An IP that is also a vendor's leads to
// that vendor, and so does any other IP when alias is a vendor's IP.
func (n *network) resolve(host string, alias netip.Addr, ips ...netip.Addr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.names[host] = ips
	for _, ip := range ips {
		for _, port := range []string{"80", "443", "8080", "8443"} {
			if local, found := n.routes[net.JoinHostPort(alias.String(), port)]; found {
				n.routes[net.JoinHostPort(ip.String(), port)] = local
			}
		}
	}
}

// forget makes host a name nobody can look up.
func (n *network) forget(host string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.names, host)
}

func (n *network) triedToDial(ip string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, address := range n.dialed {
		if host, _, _ := net.SplitHostPort(address); host == ip {
			return true
		}
	}
	return false
}

// listenLocalOn listens on addr, to stand for another program that took it.
func listenLocalOn(addr string) net.Listener {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		panic(err)
	}
	return l
}

// errPingNotAllowed is a system that does not let the proxy ping.
var errPingNotAllowed = errors.New("this system does not let the proxy ping")

// Send answers a path packet as the path to its vendor IP would. Every
// answer comes at once: silence is a packet nothing answers.
func (n *network) Send(_ context.Context, p pathprobe.Packet) pathprobe.Answer {
	n.mu.Lock()
	route, found := n.paths[p.To.Addr()]
	n.packets = append(n.packets, p)
	n.mu.Unlock()
	vendor := p.To.Addr()
	silence := pathprobe.Answer{Kind: pathprobe.Silence}
	switch {
	case !found:
		return pathprobe.Answer{Kind: pathprobe.NotSent, Err: errors.ErrUnsupported}
	case p.Transport == pathprobe.Ping && route.pingNotAllowed:
		return pathprobe.Answer{Kind: pathprobe.NotSent, Err: errPingNotAllowed}
	case p.Transport == pathprobe.Ping && route.answersPing && route.diesAfter == 0:
		return pathprobe.Answer{Kind: pathprobe.Connected, From: vendor}
	case p.Transport == pathprobe.Ping:
		return silence
	case route.diesAfter > 0 && p.TTL > route.diesAfter:
		return silence
	case p.TTL == route.prohibitedBy && (p.Transport == pathprobe.TCP || !route.answersUDP):
		return pathprobe.Answer{Kind: pathprobe.RouterError, From: route.routers[p.TTL-1], ICMPType: 3, ICMPCode: 13}
	case p.TTL == route.unreachableBy:
		return pathprobe.Answer{Kind: pathprobe.RouterError, From: route.routers[p.TTL-1], ICMPType: 3, ICMPCode: 1}
	case p.TTL <= len(route.routers) && route.routers[p.TTL-1].IsValid():
		return pathprobe.Answer{Kind: pathprobe.RouterError, From: route.routers[p.TTL-1], ICMPType: 11}
	case p.TTL <= len(route.routers):
		return silence
	case p.Transport == pathprobe.UDP && route.answersUDP:
		return pathprobe.Answer{Kind: pathprobe.RouterError, From: vendor, ICMPType: 3, ICMPCode: 3}
	}
	return silence
}
