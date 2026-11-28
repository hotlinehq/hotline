package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/cucumber/godog"
)

// talker is a vendor that speaks TLS with no HTTP on top: it reads and
// writes raw bytes, so a scenario can stop one direction on its own.
type talker struct {
	// got is what the vendor read before the app stopped sending.
	got chan string
}

// closeWriter is a connection that can stop sending and still read.
type closeWriter interface{ CloseWrite() error }

func halfCloseSteps(sc *godog.ScenarioContext, sut *captureSUT) {
	sc.Step(`^pinned.example.com answers only after the app stops sending$`, func() error {
		sut.vendors.startTalker("pinned.example.com", func(c *tls.Conn, _ chan<- string) {
			got, _ := io.ReadAll(c)
			_, _ = io.WriteString(c, "RESPONSE to "+string(got))
		})
		return nil
	})
	sc.Step(`^pinned.example.com sends "([^"]*)", stops sending, then reads what comes$`, func(greeting string) error {
		sut.vendors.startTalker("pinned.example.com", func(c *tls.Conn, got chan<- string) {
			_, _ = io.WriteString(c, greeting)
			stopSending(c)
			b, _ := io.ReadAll(c)
			got <- string(b)
		})
		return nil
	})
	sc.Step(`^the app sends "([^"]*)" through (.+) to (\S+), then stops sending$`, func(text, kind, host string) error {
		app, err := sut.relayedTunnel(kind, host)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(app, text); err != nil {
			return err
		}
		stopSending(app)
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		sut.app.respBody, sut.app.respErr = io.ReadAll(app)
		return nil
	})
	sc.Step(`^the app opens (.+) to (\S+), sending nothing yet$`, func(kind, host string) error {
		app, err := sut.relayedTunnel(kind, host)
		sut.app.tunnel = app
		return err
	})
	sc.Step(`^the app sends "([^"]*)" through the tunnel, then stops sending$`, func(text string) error {
		app := sut.app.tunnel.(*tls.Conn)
		if _, err := io.WriteString(app, text); err != nil {
			return err
		}
		stopSending(app)
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		sut.app.respBody, sut.app.respErr = io.ReadAll(app)
		return nil
	})
	sc.Step(`^the app gets "([^"]*)"$`, func(want string) error {
		if string(sut.app.respBody) != want {
			return failf("the app got %q (%v)", sut.app.respBody, sut.app.respErr)
		}
		return nil
	})
	sc.Step(`^the app reads "([^"]*)" through (.+) from (\S+), then sends "([^"]*)" and stops sending$`, func(want, kind, host, text string) error {
		app, err := sut.relayedTunnel(kind, host)
		if err != nil {
			return err
		}
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, _ := io.ReadAll(app)
		if string(got) != want {
			return failf("the app got %q", got)
		}
		_, _ = io.WriteString(app, text)
		stopSending(app)
		return nil
	})
	sc.Step(`^the app reads "([^"]*)" through (.+) from (\S+)$`, func(want, kind, host string) error {
		app, err := sut.relayedTunnel(kind, host)
		if err != nil {
			return err
		}
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, _ := io.ReadAll(app)
		if string(got) != want {
			return failf("the app got %q", got)
		}
		sut.app.tunnel = app
		return nil
	})
	sc.Step(`^the tunnel ends$`, func() error {
		// Under TLS, the tunnel itself must end too, not only the vendor's
		// TLS.
		raw := sut.app.tunnel.(*tls.Conn).NetConn()
		_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := io.Copy(io.Discard, raw)
		if err != nil {
			return failf("the tunnel did not end: %v", err)
		}
		return nil
	})
	sc.Step(`^pinned.example.com got "([^"]*)"$`, func(want string) error {
		select {
		case got := <-sut.vendors.talker.got:
			if got != want {
				return failf("the vendor got %q", got)
			}
			return nil
		case <-time.After(5 * time.Second):
			return failf("the vendor never finished reading")
		}
	})
}

// startTalker makes host a talker on port 443, which runs talk on the one
// connection it takes, over TLS.
func (sut *vendorsSUT) startTalker(host string, talk func(c *tls.Conn, got chan<- string)) {
	cert := sut.authority.leaf(host, time.Now().Add(24*time.Hour), false)
	sut.startPlainTalker(host, "443", func(raw net.Conn, got chan<- string) {
		c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		defer func() { _ = c.Close() }()
		talk(c, got)
	})
}

// startPlainTalker makes host a server on port, which runs talk on the one
// connection it takes, as plain bytes, and then closes it.
func (sut *vendorsSUT) startPlainTalker(host, port string, talk func(c net.Conn, got chan<- string)) {
	ip := netip.MustParseAddr("192.0.2.250")
	listener := listenLocal()
	sut.proxy.opened = append(sut.proxy.opened, listener)
	sut.network.name(host, ip)
	sut.network.route(net.JoinHostPort(ip.String(), port), listener.Addr().String())
	sut.talker = &talker{got: make(chan string, 1)}
	got := sut.talker.got
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		talk(c, got)
	}()
}

// relayedTunnel opens a tunnel of the kind to host:443, which the proxy
// relays untouched, and sets up TLS with the vendor inside it.
func (sut *captureSUT) relayedTunnel(kind, host string) (*tls.Conn, error) {
	if err := sut.proxy.start(); err != nil {
		return nil, err
	}
	authority := net.JoinHostPort(host, "443")
	var conn net.Conn
	var err error
	switch kind {
	case "a tunnel opened over HTTP/1.1":
		conn, err = sut.app.dialProxy()
		if err == nil {
			err = sut.sendConnect(conn, authority, nil)
		}
	case "a captured tunnel":
		conn, err = sut.dialCapture()
		if err == nil {
			err = sut.sendConnect(conn, authority, captureHeaders("4242", "/usr/bin/nc"))
		}
	case "a tunnel opened over HTTP/2":
		conn, err = sut.streamTunnel(authority)
	default:
		return nil, godog.ErrPending
	}
	if conn != nil {
		sut.app.tunnels = append(sut.app.tunnels, conn)
	}
	if err != nil {
		return nil, err
	}
	if sut.app.resp.StatusCode != http.StatusOK {
		return nil, failf("the proxy answered CONNECT with %d", sut.app.resp.StatusCode)
	}
	roots := x509.NewCertPool()
	roots.AddCert(sut.vendors.authority.cert)
	app := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return app, app.HandshakeContext(ctx)
}

// streamTunnel opens a tunnel as one stream of an HTTP/2 connection.
func (sut *captureSUT) streamTunnel(authority string) (net.Conn, error) {
	if sut.app.h2 == nil {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		sut.app.h2 = &http.Transport{
			Protocols: protocols,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, sut.proxy.addr)
			},
		}
	}
	body, app := io.Pipe()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodConnect, "http://"+authority, body)
	resp, err := sut.app.h2.RoundTrip(req) //nolint:bodyclose // the response is the tunnel, closed with it
	if err != nil {
		return nil, err
	}
	sut.app.resp = resp
	return &streamConn{response: resp.Body, body: app}, nil
}

// stopSending tells the other side nothing more comes, at the TLS level
// and then on the connection under it, and keeps reading.
func stopSending(c *tls.Conn) {
	_ = c.CloseWrite()
	if raw, ok := c.NetConn().(closeWriter); ok {
		_ = raw.CloseWrite()
	}
}
