package proxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// errTLSSetup is a TLS connection to the proxy that failed to set up;
// appSUT.tlsErr says why.
var errTLSSetup = errors.New("TLS to the proxy failed to set up")

// appSUT is the apps that call through the proxy: the call they make unless
// a step says otherwise, how they reach the proxy, and what came back.
type appSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	// appID is the integration ID apps send; empty sends none.
	appID string
	// method and target are the call apps make unless a step says
	// otherwise.
	method string
	target string
	// lastSent are the headers of the app's last call, sentBody its body
	// when it sent one, and sent counts the calls sent through the proxy.
	lastSent []string
	sentBody string
	sent     int
	// pending is a call a Given arranged for a later When, and callDone gets
	// the result of a call started in the background. freed, when closed,
	// lets the first of two calls end.
	pending  *request
	callDone chan error
	freed    chan struct{}
	appTook  time.Duration

	resp     *http.Response
	respBody []byte
	respErr  error

	// appTLS, when set, makes apps open TLS to the proxy with it. tlsState
	// is what the app's last TLS connection agreed on, and tlsErr why
	// setting it up failed.
	appTLS   *tls.Config
	tlsState tls.ConnectionState
	tlsErr   error
	// openConn is an app connection kept open between steps.
	openConn net.Conn
	// tunnel is the last tunnel the app opened, and tunnels all of them,
	// closed with the scenario. tunnelErr says why TLS inside the last one
	// failed, and tunnelCerts are the certificates the proxy showed in each.
	tunnel      net.Conn
	tunnels     []net.Conn
	tunnelErr   error
	tunnelCerts []*x509.Certificate
	// tunnelRoots are the certificate authorities an app trusts inside a
	// tunnel, and tunnelProtos the HTTP versions it offers there; none
	// means both.
	tunnelRoots  *x509.CertPool
	tunnelProtos []string
	// h2 is the app's HTTP/2 connection for tunnels, and h2Reused says the
	// last tunnel shared it.
	h2       *http.Transport
	h2Reused bool
	// transparentHost is the site the app's last connection to the
	// transparent or capture listener named.
	transparentHost string
}

func newAppSUT(root *proxySUT, vendors *vendorsSUT) *appSUT {
	return &appSUT{
		proxy:   root,
		vendors: vendors,
		appID:   "talon-shop-a",
		method:  "PUT",
		target:  "https://api.talon.one/v2/customer_sessions/sess-8812?dry=true",
	}
}

// Close closes the connections the app keeps open.
func (sut *appSUT) Close() {
	for _, c := range sut.tunnels {
		_ = c.Close()
	}
	if sut.h2 != nil {
		sut.h2.CloseIdleConnections()
	}
	if sut.openConn != nil {
		_ = sut.openConn.Close()
	}
}

// request is a raw call from an app to the proxy port.
type request struct {
	method  string
	target  string
	headers []string
	body    io.Reader
	// length, when not -1, is the Content-Length to send.
	length int
	// chunked sends the body in chunks as it is read.
	chunked bool
	// trailers are sent after a chunked body, as "Name: value" lines.
	trailers []string
	// hangUp half-closes the connection once the body is sent.
	hangUp bool
	// hangUpAfter closes the connection after that long without reading the
	// answer.
	hangUpAfter time.Duration
}

func basic(credentials string) string {
	return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
}

// defaultCall makes the world's usual call.
func (sut *appSUT) defaultCall(headers ...string) error {
	return sut.call(sut.method, sut.target, headers...)
}

// call sends a request the way an app does, with the world's integration ID.
func (sut *appSUT) call(method, target string, headers ...string) error {
	if sut.appID != "" {
		headers = append([]string{basic(sut.appID + ":x")}, headers...)
	}
	return sut.send(request{method: method, target: target, headers: headers, length: -1})
}

// headerValue is the value of the first "Name: value" line for name.
func headerValue(headers []string, name string) string {
	for _, h := range headers {
		if key, value, found := strings.Cut(h, ":"); found && strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// hostOf is the vendor host in a request target.
func hostOf(target string) string {
	host := target
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	return host
}

func (sut *appSUT) send(r request) error {
	sut.lastSent = r.headers
	if err := sut.proxy.start(); err != nil {
		return err
	}
	host := strings.ToLower(hostOf(r.target))
	if host == "" {
		// A Host header call names its vendor in the Host header.
		host = strings.ToLower(headerValue(r.headers, "Host"))
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if _, err := netip.ParseAddr(host); err != nil && !sut.vendors.missing[host] && host != "" {
		sut.vendors.vendor(host)
	}
	conn, err := sut.dialProxy()
	if errors.Is(err, errTLSSetup) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	sut.sent++
	return sut.exchange(conn, r)
}

// dialProxy opens a connection to the proxy port, with TLS when the app
// uses it. When TLS fails to set up, the error is errTLSSetup and tlsErr
// says why.
func (sut *appSUT) dialProxy() (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", sut.proxy.addr)
	if err != nil || sut.appTLS == nil {
		return conn, err
	}
	client := tls.Client(conn, sut.appTLS)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	sut.tlsErr = client.HandshakeContext(context.Background())
	if sut.tlsErr != nil {
		_ = conn.Close()
		return nil, errTLSSetup
	}
	_ = client.SetDeadline(time.Time{})
	sut.tlsState = client.ConnectionState()
	return client, nil
}

// exchange sends one request on an open connection and reads the answer.
func (sut *appSUT) exchange(conn net.Conn, r request) error {
	var head bytes.Buffer
	fmt.Fprintf(&head, "%s %s HTTP/1.1\r\n", r.method, r.target)
	hasHost := false
	for _, h := range r.headers {
		hasHost = hasHost || strings.HasPrefix(strings.ToLower(h), "host:")
		head.WriteString(h + "\r\n")
	}
	if !hasHost {
		head.WriteString("Host: " + hostOf(r.target) + "\r\n")
	}
	switch {
	case r.chunked:
		head.WriteString("Transfer-Encoding: chunked\r\n")
		for _, t := range r.trailers {
			name, _, _ := strings.Cut(t, ":")
			head.WriteString("Trailer: " + name + "\r\n")
		}
	case r.length >= 0:
		fmt.Fprintf(&head, "Content-Length: %d\r\n", r.length)
	}
	head.WriteString("\r\n")
	if _, err := conn.Write(head.Bytes()); err != nil {
		return err
	}
	if r.body != nil {
		go sut.writeBody(conn, r)
	}
	if r.hangUpAfter > 0 {
		pause(r.hangUpAfter)
		return nil
	}
	return sut.readAnswer(conn)
}

// readAnswer reads the proxy's answer to the request just sent.
func (sut *appSUT) readAnswer(conn net.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	sut.resp, sut.respErr = resp, err
	if err == nil {
		sut.respBody, sut.respErr = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	return nil
}

func (sut *appSUT) writeBody(conn net.Conn, r request) {
	buf := make([]byte, 16*1024)
	for {
		n, err := r.body.Read(buf)
		if n > 0 && r.chunked {
			_, _ = fmt.Fprintf(conn, "%x\r\n%s\r\n", n, buf[:n])
		} else if n > 0 {
			_, _ = conn.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if r.chunked {
		_, _ = io.WriteString(conn, "0\r\n")
		for _, t := range r.trailers {
			_, _ = io.WriteString(conn, t+"\r\n")
		}
		_, _ = io.WriteString(conn, "\r\n")
	} else if tcp, ok := conn.(*net.TCPConn); ok && r.hangUp {
		_ = tcp.CloseWrite()
	}
}

// post sends a call with a body and headers, the way the app talon-shop-a
// does.
func (sut *appSUT) post(target, contentType, body string, headers ...string) error {
	r := request{method: "POST", target: target, length: len(body),
		headers: append([]string{basic("talon-shop-a:x"), "Content-Type: " + contentType}, headers...), body: strings.NewReader(body)}
	return sut.send(r)
}
