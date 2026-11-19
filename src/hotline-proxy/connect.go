package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"hash/maphash"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/config"
)

// tunnel is what a CONNECT named: the vendor host and port, and the
// integration ID, which covers every call inside the tunnel.
type tunnel struct {
	host, port, id string
	// ingress is what the records of its calls say: connect when empty.
	ingress string
	// process is the program that made the calls, when the ingress knows.
	process process
}

// process is the program behind a call, as an ingress that can tell reports
// it: the process number and the path of the program. Either is zero when
// not known.
type process struct {
	pid  int
	path string
}

// attributes is what the record says about the program: nothing for the
// parts not known.
func (p process) attributes() []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if p.pid > 0 {
		attrs = append(attrs, semconv.ProcessPID(p.pid))
	}
	if p.path != "" {
		attrs = append(attrs, semconv.ProcessExecutablePath(p.path))
	}
	return attrs
}

// connect opens a tunnel for an app. The proxy answers 200, then sets up
// TLS inside the tunnel itself, showing a certificate for the host the
// tunnel names, signed by the interception certificate authority. A tunnel whose first
// byte is not TLS carries plain HTTP. Either way the tunnel is then handed
// to the HTTP server like any other app connection, and each call in it is
// a call on the CONNECT ingress.
func (h *handler) connect(w http.ResponseWriter, r *http.Request, start time.Time, cfg *config.Config, li *listening) {
	t, ok := parseTunnel(r, cfg)
	if !ok {
		refuse(w, http.StatusBadRequest)
		h.refused(r, start, cfg, "connect", t.id, &url.URL{Host: t.host}, http.StatusBadRequest)
		return
	}
	var err error
	conn, done := open(w, r)
	if li.settings(cfg).Connect.Passthrough.Passes(t.host) {
		h.passedThrough(conn, t, start, cfg)
		<-done
		return
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	reader := bufio.NewReader(conn)
	inner := &bufferedConn{Conn: conn, r: reader}
	// The app's first byte tells TLS, which starts with a handshake
	// record, from plain HTTP. An app that leaves, or the proxy stopping,
	// ends the wait.
	stop := context.AfterFunc(h.tunnels.ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	first, _ := reader.Peek(1)
	stop()
	a := &appConn{Conn: inner, tunnel: t}
	if len(first) == 1 && first[0] == 0x16 {
		secure := tls.Server(inner, li.signer.tlsConfig(t.host))
		a.Conn, a.secure = secure, true
		a.handshake, err = timeHandshake(h.tunnels.ctx, secure, h.clock)
		if err != nil {
			h.tunnelFailed(r, start, cfg, t, a, err)
			_ = conn.Close()
		}
	}
	if err == nil {
		_ = conn.SetDeadline(time.Time{})
		h.tunnels.hand(a)
	}
	<-done
}

// parseTunnel reads the host and port a CONNECT names, and its integration
// ID as cfg knows it. The ID is judged once, for the whole tunnel: a reload
// that adds or removes the integration applies from the next connection,
// as passthrough changes do. A host or port that is not usable is refused.
func parseTunnel(r *http.Request, cfg *config.Config) (*tunnel, bool) {
	t := &tunnel{id: integrationID(r.Header, cfg)}
	var err error
	t.host, t.port, err = net.SplitHostPort(r.Host)
	if port, _ := strconv.Atoi(t.port); err != nil || t.host == "" || port < 1 || port > 65535 {
		return t, false
	}
	t.host = strings.ToLower(t.host)
	return t, true
}

// open answers 200 to a CONNECT and gives the tunnel as a connection.
// Over HTTP/1.1 the tunnel is the app's connection, taken over from the
// HTTP server, and done is already closed. Over HTTP/2, which cannot give
// up a connection that other calls share, the tunnel is the CONNECT's own
// stream, and done closes once the stream has ended.
func open(w http.ResponseWriter, r *http.Request) (net.Conn, <-chan struct{}) {
	conn, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return stream(w, r)
	}
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	// Bytes the HTTP server read ahead come first; the rest comes
	// straight from the connection.
	early, _ := buffered.Peek(buffered.Reader.Buffered())
	done := make(chan struct{})
	close(done)
	return &bufferedConn{Conn: conn, r: io.MultiReader(bytes.NewReader(early), conn)}, done
}

// stream carries a tunnel in a CONNECT's HTTP/2 stream: the app's bytes
// come in the request body and the proxy's go out in the response. Each
// direction is a pipe of its own, so the app ending its body ends only what
// the tunnel reads, and the response goes on. The other way round is not
// possible: the response ends only when the whole stream does, after which
// the body cannot be read. So the stream ends once the tunnel is closed.
func stream(w http.ResponseWriter, r *http.Request) (net.Conn, <-chan struct{}) {
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	in, fromApp := net.Pipe()
	toApp, out := net.Pipe()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(fromApp, r.Body)
		_ = fromApp.Close()
	}()
	go func() {
		defer close(done)
		_, _ = io.Copy(flushWriter{w: w, rc: rc}, toApp)
		// A response the app no longer takes fails the tunnel's next write.
		_ = toApp.Close()
	}()
	// The pipes have no address of their own: calls inside the tunnel come
	// from the app's.
	from, _ := netip.ParseAddrPort(r.RemoteAddr)
	return &streamTunnel{in: in, out: out, remote: net.TCPAddrFromAddrPort(from)}, done
}

// streamTunnel is a tunnel over an HTTP/2 stream, with the app's address.
// It reads from one pipe and writes to another, so the app can stop
// sending while the response goes on.
type streamTunnel struct {
	in, out net.Conn
	remote  net.Addr
}

func (c *streamTunnel) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c *streamTunnel) Write(p []byte) (int, error) { return c.out.Write(p) }
func (c *streamTunnel) Close() error                { return errors.Join(c.in.Close(), c.out.Close()) }
func (c *streamTunnel) LocalAddr() net.Addr         { return c.in.LocalAddr() }
func (c *streamTunnel) RemoteAddr() net.Addr        { return c.remote }
func (c *streamTunnel) SetDeadline(t time.Time) error {
	return errors.Join(c.in.SetReadDeadline(t), c.out.SetWriteDeadline(t))
}
func (c *streamTunnel) SetReadDeadline(t time.Time) error  { return c.in.SetReadDeadline(t) }
func (c *streamTunnel) SetWriteDeadline(t time.Time) error { return c.out.SetWriteDeadline(t) }

// flushWriter sends each write to the app at once.
type flushWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.rc.Flush()
	}
	return n, err
}

// tunnelFailed records a tunnel in which TLS failed to set up, with what
// the CONNECT said, so the record tells which app is missing the certificate authority.
func (h *handler) tunnelFailed(r *http.Request, start time.Time, cfg *config.Config, t *tunnel, a *appConn, err error) {
	h.logger.Debug("app TLS setup inside a tunnel failed", "host", t.host, "reason", err.Error())
	h.own.tlsFailed(err, attribute.String("hotline.ingress", "connect"))
	// No HTTP call was sent inside the tunnel, so the record has no body
	// hashes.
	s := short{
		id: t.id, ingress: "connect", client: clientIP(r.RemoteAddr), host: t.host, method: r.Method,
		errorType: untrustedOr(err), header: r.Header, start: start, cfg: cfg,
	}
	if from, to, timed := a.handshakeTime(); timed {
		s.appTLS = &[2]time.Time{from, to}
	}
	h.recordShort(s)
}

// find works out the vendor address of a call inside the tunnel. The call
// goes to the host and port the CONNECT named, so its Host must name the
// same; a call for another host gets 421, as HTTP says for a call sent on
// the wrong connection.
func (t *tunnel) find(r *http.Request, a *appConn) (*url.URL, int) {
	scheme, defaultPort := "http", "80"
	if a.secure {
		scheme, defaultPort = "https", "443"
	}
	target := &url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	port := target.Port()
	if port == "" {
		port = defaultPort
	}
	if !strings.EqualFold(target.Hostname(), t.host) || port != t.port {
		return target, http.StatusMisdirectedRequest
	}
	return target, 0
}

// bufferedConn reads what the HTTP server had already read from the
// connection before the rest.
type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite tells the app nothing more comes and keeps reading, when the
// connection under it can do that; otherwise it closes the connection.
func (c *bufferedConn) CloseWrite() error {
	closeWrite(c.Conn)
	return nil
}

// leafLife is how long a certificate the proxy makes for a vendor host is
// valid, and leafRenew how long before its end the proxy makes a new one.
const (
	leafLife  = 24 * time.Hour
	leafRenew = time.Hour
	// leafSlots bounds how many certificates the proxy keeps. Each host
	// has one slot, picked by a hash of its name; two hosts that share a
	// slot take turns, and a certificate pushed out is made again.
	leafSlots = 4096
)

// interceptor makes the certificates the proxy shows apps inside tunnels:
// one per vendor host, signed by the interception certificate authority, kept in memory
// only and reused until shortly before it ends or the certificate authority changes.
type interceptor struct {
	authority *certHolder
	clock     Clock
	logger    *slog.Logger
	seed      maphash.Seed

	mu sync.Mutex
	// signer is the certificate authority the kept certificates were signed with.
	signer *tls.Certificate
	leaves map[uint64]leafSlot
}

// leafSlot is a kept certificate and the host it is for.
type leafSlot struct {
	host string
	cert *tls.Certificate
}

func newInterceptor(t *config.TLS, clock Clock, logger *slog.Logger) *interceptor {
	i := &interceptor{
		authority: newCertHolder(t, logger, "new interception certificate authority in use"),
		clock:     clock,
		logger:    logger,
		seed:      maphash.MakeSeed(),
		signer:    t.Certificate,
		leaves:    map[uint64]leafSlot{},
	}
	i.checkLife(t.Certificate)
	return i
}

// checkLife warns about a certificate authority valid for more than a year: whoever
// holds its key can pose as any website to every app that trusts it, so
// it is best kept short-lived.
func (i *interceptor) checkLife(authority *tls.Certificate) {
	if authority.Leaf.NotAfter.Sub(authority.Leaf.NotBefore) > 365*24*time.Hour {
		i.logger.Warn("the interception certificate authority is valid for more than a year; a shorter one limits the harm if its key leaks",
			"not_after", authority.Leaf.NotAfter)
	}
}

// tlsConfig is the TLS setup inside a tunnel to host: the same versions
// and HTTP versions as the proxy port, with a certificate for host.
func (i *interceptor) tlsConfig(host string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return i.leaf(host)
		},
	}
}

// leaf is the certificate for host, made on first use.
func (i *interceptor) leaf(host string) (*tls.Certificate, error) {
	authority := i.authority.get()
	now := i.clock.Now()
	i.mu.Lock()
	defer i.mu.Unlock()
	if authority != i.signer {
		i.checkLife(authority)
		i.signer, i.leaves = authority, map[uint64]leafSlot{}
	}
	slot := maphash.String(i.seed, host) % leafSlots
	if kept := i.leaves[slot]; kept.host == host && now.Before(kept.cert.Leaf.NotAfter.Add(-leafRenew)) {
		return kept.cert, nil
	}
	cert, err := sign(authority, host, now)
	if err == nil {
		i.leaves[slot] = leafSlot{host: host, cert: cert}
	}
	return cert, err
}

// sign makes a certificate for host, valid from an hour ago, to allow for
// clocks that differ a little, for leafLife, and never beyond the certificate authority.
// Its key is new and never leaves memory.
func sign(authority *tls.Certificate, host string, now time.Time) (*tls.Certificate, error) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // crypto/rand never fails
	serial := make([]byte, 16)
	_, _ = rand.Read(serial) // crypto/rand never fails
	tmpl := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes(serial),
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     slices.MinFunc([]time.Time{now.Add(leafLife), authority.Leaf.NotAfter}, time.Time.Compare),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		tmpl.IPAddresses = []net.IP{ip.AsSlice()}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, authority.Leaf, &key.PublicKey, authority.PrivateKey)
	leaf, _ := x509.ParseCertificate(der) // nil when signing failed
	return &tls.Certificate{Certificate: append([][]byte{der}, authority.Certificate...), PrivateKey: key, Leaf: leaf}, err
}
