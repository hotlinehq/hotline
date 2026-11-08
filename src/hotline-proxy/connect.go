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
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/tracectx"
)

// tunnel is what a CONNECT named: the vendor host and port, and the
// integration ID, which covers every call inside the tunnel.
type tunnel struct {
	host, port, id string
	// ingress is what the records of its calls say: connect when empty.
	ingress string
}

// connect opens a tunnel for an app. The proxy answers 200, then sets up
// TLS inside the tunnel itself, showing a certificate for the host the
// tunnel names, signed by the interception issuer. A tunnel whose first
// byte is not TLS carries plain HTTP. Either way the tunnel is then handed
// to the HTTP server like any other app connection, and each call in it is
// a call on the CONNECT ingress.
func (h *handler) connect(w http.ResponseWriter, r *http.Request, start time.Time) {
	t := &tunnel{id: integrationID(r.Header)}
	var err error
	t.host, t.port, err = net.SplitHostPort(r.Host)
	if port, _ := strconv.Atoi(t.port); err != nil || t.host == "" || port < 1 || port > 65535 {
		refuse(w, http.StatusBadRequest)
		h.refused(r, start, "connect", t.id, &url.URL{Host: t.host}, http.StatusBadRequest)
		return
	}
	t.host = strings.ToLower(t.host)
	conn, done := open(w, r)
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
		secure := tls.Server(inner, h.intercept.tlsConfig(t.host))
		a.Conn, a.secure = secure, true
		a.handshake, err = timeHandshake(h.tunnels.ctx, secure, h.clock)
		if err != nil {
			h.tunnelFailed(r, start, t, a, err)
			_ = conn.Close()
		}
	}
	if err == nil {
		_ = conn.SetDeadline(time.Time{})
		h.tunnels.hand(a)
	}
	<-done
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
// come in the request body and the proxy's go out in the reply. The tunnel
// is one end of a pipe; the bytes are copied between its other end and the
// stream. The stream ends once the tunnel is closed, or the app ends its
// side, which closes the tunnel.
func stream(w http.ResponseWriter, r *http.Request) (net.Conn, <-chan struct{}) {
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	tunnel, end := net.Pipe()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(end, r.Body)
		_ = end.Close()
	}()
	go func() {
		defer close(done)
		_, _ = io.Copy(flushWriter{w: w, rc: rc}, end)
	}()
	// The pipe has no address of its own: calls inside the tunnel come
	// from the app's.
	from, _ := netip.ParseAddrPort(r.RemoteAddr)
	return fromApp{Conn: tunnel, remote: net.TCPAddrFromAddrPort(from)}, done
}

// fromApp is a tunnel over an HTTP/2 stream, with the app's address.
type fromApp struct {
	net.Conn
	remote net.Addr
}

func (c fromApp) RemoteAddr() net.Addr { return c.remote }

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
// the CONNECT said, so the record tells which app is missing the issuer.
func (h *handler) tunnelFailed(r *http.Request, start time.Time, t *tunnel, a *appConn, err error) {
	h.logger.Debug("app TLS setup inside a tunnel failed", "host", t.host, "reason", err.Error())
	h.own.tlsFailed(err, attribute.String("hotline.ingress", "connect"))
	errorType := untrustedOr(err)
	attrs := []attribute.KeyValue{
		attribute.String("hotline.integration.id", orNotPassed(t.id)),
		attribute.String("hotline.ingress", "connect"),
		clientAddress(r),
		semconv.ServerAddress(t.host),
		semconv.HTTPRequestMethodKey.String(r.Method),
		semconv.ErrorTypeKey.String(errorType),
	}
	end := h.clock.Now()
	traced, parent, _ := h.traced(t.id, r.Header, start)
	spans := []export.Span{callSpan(traced, parent, start, end, true)}
	if from, to, timed := a.handshakeTime(); timed {
		spans = append(spans, export.Span{ID: tracectx.NewSpanID(), Parent: traced.SpanID(), Name: "app tls handshake",
			Kind: trace.SpanKindInternal, Start: from, End: to, Failed: true})
	}
	attrs = append(attrs, attribute.Int64("hotline.timing.total_us", end.Sub(start).Microseconds()))
	h.record(r, finished{sc: traced, at: start, attrs: attrs, spans: spans})
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
// one per vendor host, signed by the interception issuer, kept in memory
// only and reused until shortly before it ends or the issuer changes.
type interceptor struct {
	issuer *certHolder
	clock  Clock
	logger *slog.Logger
	seed   maphash.Seed

	mu sync.Mutex
	// signer is the issuer the kept certificates were signed with.
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
		issuer: newCertHolder(t, logger, "new interception issuer in use"),
		clock:  clock,
		logger: logger,
		seed:   maphash.MakeSeed(),
		signer: t.Certificate,
		leaves: map[uint64]leafSlot{},
	}
	i.checkLife(t.Certificate)
	return i
}

// checkLife warns about an issuer valid for more than a year: whoever
// holds its key can pose as any website to every app that trusts it, so
// it is best kept short-lived.
func (i *interceptor) checkLife(issuer *tls.Certificate) {
	if issuer.Leaf.NotAfter.Sub(issuer.Leaf.NotBefore) > 365*24*time.Hour {
		i.logger.Warn("the interception issuer is valid for more than a year; a shorter one limits the harm if its key leaks",
			"not_after", issuer.Leaf.NotAfter)
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
	issuer := i.issuer.get()
	now := i.clock.Now()
	i.mu.Lock()
	defer i.mu.Unlock()
	if issuer != i.signer {
		i.checkLife(issuer)
		i.signer, i.leaves = issuer, map[uint64]leafSlot{}
	}
	slot := maphash.String(i.seed, host) % leafSlots
	if kept := i.leaves[slot]; kept.host == host && now.Before(kept.cert.Leaf.NotAfter.Add(-leafRenew)) {
		return kept.cert, nil
	}
	cert, err := sign(issuer, host, now)
	if err == nil {
		i.leaves[slot] = leafSlot{host: host, cert: cert}
	}
	return cert, err
}

// sign makes a certificate for host, valid from an hour ago, to allow for
// clocks that differ a little, for leafLife, and never beyond the issuer.
// Its key is new and never leaves memory.
func sign(issuer *tls.Certificate, host string, now time.Time) (*tls.Certificate, error) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader) // crypto/rand never fails
	serial := make([]byte, 16)
	_, _ = rand.Read(serial) // crypto/rand never fails
	tmpl := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes(serial),
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     slices.MinFunc([]time.Time{now.Add(leafLife), issuer.Leaf.NotAfter}, time.Time.Compare),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		tmpl.IPAddresses = []net.IP{ip.AsSlice()}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer.Leaf, &key.PublicKey, issuer.PrivateKey)
	leaf, _ := x509.ParseCertificate(der) // nil when signing failed
	return &tls.Certificate{Certificate: append([][]byte{der}, issuer.Certificate...), PrivateKey: key, Leaf: leaf}, err
}
