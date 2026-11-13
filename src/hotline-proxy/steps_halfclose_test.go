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

func halfCloseSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^pinned.example.com answers only after the app stops sending$`, func() error {
		w.startTalker("pinned.example.com", func(c *tls.Conn, _ chan<- string) {
			got, _ := io.ReadAll(c)
			_, _ = io.WriteString(c, "REPLY to "+string(got))
		})
		return nil
	})
	sc.Step(`^pinned.example.com sends "([^"]*)", stops sending, then reads what comes$`, func(greeting string) error {
		w.startTalker("pinned.example.com", func(c *tls.Conn, got chan<- string) {
			_, _ = io.WriteString(c, greeting)
			stopSending(c)
			b, _ := io.ReadAll(c)
			got <- string(b)
		})
		return nil
	})
	sc.Step(`^the app sends "([^"]*)" through (.+) to (\S+), then stops sending$`, func(text, kind, host string) error {
		app, err := w.relayedTunnel(kind, host)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(app, text); err != nil {
			return err
		}
		stopSending(app)
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		w.respBody, w.respErr = io.ReadAll(app)
		return nil
	})
	sc.Step(`^the app gets "([^"]*)"$`, func(want string) error {
		if string(w.respBody) != want {
			return failf("the app got %q (%v)", w.respBody, w.respErr)
		}
		return nil
	})
	sc.Step(`^the app reads "([^"]*)" through (.+) from (\S+), then sends "([^"]*)" and stops sending$`, func(want, kind, host, text string) error {
		app, err := w.relayedTunnel(kind, host)
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
		app, err := w.relayedTunnel(kind, host)
		if err != nil {
			return err
		}
		_ = app.SetReadDeadline(time.Now().Add(5 * time.Second))
		got, _ := io.ReadAll(app)
		if string(got) != want {
			return failf("the app got %q", got)
		}
		w.tunnel = app
		return nil
	})
	sc.Step(`^the tunnel ends$`, func() error {
		// Under TLS, the tunnel itself must end too, not only the vendor's
		// TLS.
		raw := w.tunnel.(*tls.Conn).NetConn()
		_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := io.Copy(io.Discard, raw)
		if err != nil {
			return failf("the tunnel did not end: %v", err)
		}
		return nil
	})
	sc.Step(`^pinned.example.com got "([^"]*)"$`, func(want string) error {
		select {
		case got := <-w.talker.got:
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
// connection it takes.
func (w *world) startTalker(host string, talk func(c *tls.Conn, got chan<- string)) {
	ip := netip.MustParseAddr("192.0.2.250")
	listener := listenLocal()
	w.listeners = append(w.listeners, listener)
	w.network.name(host, ip)
	w.network.route(net.JoinHostPort(ip.String(), "443"), listener.Addr().String())
	cert := w.issuer.leaf(host, time.Now().Add(24*time.Hour), false)
	w.talker = &talker{got: make(chan string, 1)}
	got := w.talker.got
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		c := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		talk(c, got)
	}()
}

// relayedTunnel opens a tunnel of the kind to host:443, which the proxy
// relays untouched, and sets up TLS with the vendor inside it.
func (w *world) relayedTunnel(kind, host string) (*tls.Conn, error) {
	if err := w.start(); err != nil {
		return nil, err
	}
	authority := net.JoinHostPort(host, "443")
	var conn net.Conn
	var err error
	switch kind {
	case "a tunnel opened over HTTP/1.1":
		conn, err = w.dialProxy()
		if err == nil {
			err = w.sendConnect(conn, authority, nil)
		}
	case "a captured tunnel":
		conn, err = w.dialCapture()
		if err == nil {
			err = w.sendConnect(conn, authority, captureHeaders("4242", "/usr/bin/nc"))
		}
	case "a tunnel opened over HTTP/2":
		conn, err = w.streamTunnel(authority)
	default:
		return nil, godog.ErrPending
	}
	if conn != nil {
		w.tunnels = append(w.tunnels, conn)
	}
	if err != nil {
		return nil, err
	}
	if w.resp.StatusCode != http.StatusOK {
		return nil, failf("the proxy answered CONNECT with %d", w.resp.StatusCode)
	}
	roots := x509.NewCertPool()
	roots.AddCert(w.issuer.cert)
	app := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return app, app.HandshakeContext(ctx)
}

// streamTunnel opens a tunnel as one stream of an HTTP/2 connection.
func (w *world) streamTunnel(authority string) (net.Conn, error) {
	if w.h2 == nil {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		w.h2 = &http.Transport{
			Protocols: protocols,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, w.addr)
			},
		}
	}
	body, app := io.Pipe()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodConnect, "http://"+authority, body)
	resp, err := w.h2.RoundTrip(req) //nolint:bodyclose // the reply is the tunnel, closed with it
	if err != nil {
		return nil, err
	}
	w.resp = resp
	return &streamConn{reply: resp.Body, body: app}, nil
}

// stopSending tells the other side nothing more comes, at the TLS level
// and then on the connection under it, and keeps reading.
func stopSending(c *tls.Conn) {
	_ = c.CloseWrite()
	if raw, ok := c.NetConn().(closeWriter); ok {
		_ = raw.CloseWrite()
	}
}
