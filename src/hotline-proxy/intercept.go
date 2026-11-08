package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http/httptrace"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"hotline-proxy/internal/export"
)

// maxHello is the most the proxy reads to find the first message of a TLS
// handshake: one record, at most 16 KiB, and its 5 byte header.
const maxHello = 5 + 1<<14

// OriginalDestination says where a connection a firewall redirected to the
// proxy was first sent: the address and port the program called. The
// program reads it from the operating system, which only Linux can do.
type OriginalDestination func(net.Conn) (netip.AddrPort, error)

// hello is what the proxy reads from the first message of a TLS handshake.
type hello struct {
	// name is the site name (SNI), empty when the program sent none.
	name string
	// ech says the handshake carries an encrypted client hello, whose
	// real site name is encrypted; name is then the outer name.
	ech bool
}

// errNotHello is a first message that is not a TLS client hello.
var errNotHello = errors.New("proxy: the first bytes are not a TLS client hello")

// readHello reads the first TLS record from r, without consuming it, and
// reads the site name and ECH extension from the client hello in it.
func readHello(r *bufio.Reader) (hello, error) {
	head, err := r.Peek(5)
	if err != nil || head[0] != 0x16 || head[1] != 3 {
		return hello{}, errNotHello
	}
	size := int(head[3])<<8 | int(head[4])
	if size == 0 || size > 1<<14 {
		return hello{}, errNotHello
	}
	record, err := r.Peek(5 + size)
	if err != nil {
		return hello{}, errNotHello
	}
	return parseHello(record[5:])
}

// parseHello reads a handshake message that should be a client hello.
func parseHello(m []byte) (hello, error) {
	// type, 3 byte length, version, random.
	if len(m) < 4+2+32 || m[0] != 1 {
		return hello{}, errNotHello
	}
	m = m[4+2+32:]
	skip := func(lengthBytes int) bool {
		if len(m) < lengthBytes {
			return false
		}
		n := 0
		for _, b := range m[:lengthBytes] {
			n = n<<8 | int(b)
		}
		if len(m) < lengthBytes+n {
			return false
		}
		m = m[lengthBytes+n:]
		return true
	}
	// session ID, cipher suites, compression methods.
	if !skip(1) || !skip(2) || !skip(1) {
		return hello{}, errNotHello
	}
	if len(m) < 2 {
		return hello{}, nil // a hello with no extensions has no name
	}
	extensions := m[2:]
	if n := int(m[0])<<8 | int(m[1]); n < len(extensions) {
		extensions = extensions[:n]
	}
	var h hello
	for len(extensions) >= 4 {
		kind := int(extensions[0])<<8 | int(extensions[1])
		n := int(extensions[2])<<8 | int(extensions[3])
		if len(extensions) < 4+n {
			return hello{}, errNotHello
		}
		body := extensions[4 : 4+n]
		extensions = extensions[4+n:]
		switch kind {
		case 0: // server_name
			h.name = serverName(body)
		case 0xfe0d: // encrypted_client_hello
			h.ech = true
		}
	}
	return h, nil
}

// serverName reads the first host name of a server_name extension.
func serverName(b []byte) string {
	// list length, then entries of type, length and name.
	if len(b) < 5 || b[2] != 0 {
		return ""
	}
	n := int(b[3])<<8 | int(b[4])
	if len(b) < 5+n {
		return ""
	}
	return strings.ToLower(string(b[5 : 5+n]))
}

// serveIntercept takes the connections a firewall redirected to the
// listener, until it is closed.
func (h *handler) serveIntercept(l net.Listener, where OriginalDestination) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		h.calls.Add(1)
		go func() {
			defer h.calls.Done()
			h.intercepted(conn, where)
		}()
	}
}

// intercepted handles one redirected connection: it reads the first message
// of the handshake and decides to intercept the program's calls, to relay
// them untouched, or to refuse, recording what it did not hand on as calls.
func (h *handler) intercepted(conn net.Conn, where OriginalDestination) {
	start := h.clock.Now()
	ic := h.cfg.Load().Intercept
	if ic == nil {
		_ = conn.Close()
		return
	}
	id := ic.Integration
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	reader := bufio.NewReaderSize(conn, maxHello)
	inner := &bufferedConn{Conn: conn, r: reader}
	first, err := readHello(reader)
	if err != nil {
		h.own.tlsFailed(err, attribute.String("hotline.ingress", "intercept"))
		h.interceptRecord(conn, start, id, "", semconv.ErrorTypeKey.String("not_tls"))
		_ = conn.Close()
		return
	}
	port := "443"
	var destination netip.AddrPort
	if where != nil {
		if got, err := where(conn); err == nil {
			destination = got
			if got.Port() != 0 {
				port = strconv.Itoa(int(got.Port()))
			}
		}
	}
	host := first.name
	switch {
	case first.ech && ic.Relays(host):
		h.relay(inner, start, id, host, port, "ech")
		return
	case first.ech:
		// The real name is encrypted: answer as the outer name, offering
		// no ECH keys. The program sees ECH rejected and retries without.
		secure := tls.Server(inner, h.redirected.tlsConfig(host))
		_, _ = timeHandshake(h.tunnels.ctx, secure, h.clock)
		h.interceptRecord(conn, start, id, host, semconv.ErrorTypeKey.String("ech_rejected"))
		_ = conn.Close()
		return
	case host == "" && destination.IsValid():
		host = destination.Addr().Unmap().String()
	case host == "":
		h.interceptRecord(conn, start, id, "", semconv.ErrorTypeKey.String("no_sni"))
		_ = conn.Close()
		return
	case ic.Passes(host):
		h.relay(inner, start, id, host, port, "passthrough")
		return
	}
	t := &tunnel{host: host, port: port, id: id, ingress: "intercept"}
	secure := tls.Server(inner, h.redirected.tlsConfig(host))
	a := &appConn{Conn: secure, tunnel: t, secure: true}
	a.handshake, err = timeHandshake(h.tunnels.ctx, secure, h.clock)
	if err != nil {
		h.logger.Debug("app TLS setup on the intercept listener failed", "reason", err.Error())
		h.own.tlsFailed(err, attribute.String("hotline.ingress", "intercept"))
		h.interceptRecord(conn, start, id, host, semconv.ErrorTypeKey.String(untrustedOr(err)))
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	h.tunnels.hand(a)
}

// relay passes a connection on untouched to host, which the proxy looks up
// itself, never using the address the program chose. The record is made
// when the relay ends.
func (h *handler) relay(app *bufferedConn, start time.Time, id, host, port, reason string) {
	ctx, cancel := context.WithTimeout(h.tunnels.ctx, handshakeTimeout)
	// The dialer reports to a call's trace, which a relay has none of.
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) {},
		DNSDone:      func(httptrace.DNSDoneInfo) {},
		ConnectStart: func(string, string) {},
		ConnectDone:  func(string, string, error) {},
	})
	vendor, err := h.relays.dial(ctx, "tcp", net.JoinHostPort(host, port))
	cancel()
	mode := []attribute.KeyValue{
		attribute.String("hotline.intercept.mode", "passthrough"),
		attribute.String("hotline.intercept.reason", reason),
	}
	if err != nil {
		errorType := "destination_unavailable"
		if errors.Is(err, errProhibited) {
			errorType = "destination_ip_prohibited"
		}
		h.interceptRecord(app.Conn, start, id, host, append(mode, semconv.ErrorTypeKey.String(errorType))...)
		_ = app.Close()
		return
	}
	_ = app.SetDeadline(time.Time{})
	// Stopping the proxy ends the relay.
	stop := context.AfterFunc(h.tunnels.ctx, func() { _ = app.Close(); _ = vendor.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(vendor, app)
		closeWrite(vendor)
	}()
	_, _ = io.Copy(app, vendor)
	closeWrite(app.Conn)
	<-done
	_ = app.Close()
	_ = vendor.Close()
	h.interceptRecord(app.Conn, start, id, host, mode...)
}

// closeWrite tells the other side nothing more comes. A connection that
// cannot say that alone is closed.
func closeWrite(c net.Conn) {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		_ = w.CloseWrite()
		return
	}
	_ = c.Close()
}

// interceptRecord records a connection on the intercept listener that never
// became a call: minimal, with the host when the proxy could read one.
func (h *handler) interceptRecord(conn net.Conn, start time.Time, id, host string, extra ...attribute.KeyValue) {
	from, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	attrs := []attribute.KeyValue{
		attribute.String("hotline.integration.id", orNotPassed(id)),
		attribute.String("hotline.ingress", "intercept"),
		semconv.ClientAddress(from),
	}
	if host != "" {
		attrs = append(attrs, semconv.ServerAddress(host))
	}
	failed := false
	for _, kv := range extra {
		failed = failed || kv.Key == semconv.ErrorTypeKey
	}
	attrs = append(attrs, extra...)
	end := h.clock.Now()
	attrs = append(attrs, attribute.Int64("hotline.timing.total_us", end.Sub(start).Microseconds()))
	traced, parent, _ := h.traced(id, nil, start)
	spans := []export.Span{callSpan(traced, parent, start, end, failed)}
	h.record(nil, finished{sc: traced, at: start, attrs: attrs, spans: spans})
}
