package proxy

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// appConn is an app's connection as the HTTP server gets it: on the
// proxy's TLS port, or a tunnel opened by CONNECT. When the app sets up TLS
// with the proxy, the proxy does that itself, so the server sees the
// connection as plain and tells HTTP/1.1 from HTTP/2 by its first bytes,
// whatever the app offered while setting up TLS. Go's client, for one,
// offers HTTP/2 to an https proxy address and then speaks HTTP/1.1.
type appConn struct {
	net.Conn
	// tunnel, when set, is the CONNECT that opened this connection.
	tunnel *tunnel
	// setUp, when set, sets up TLS with the app on the first read, in the
	// server's own goroutine for the connection, so a slow app holds up
	// nobody.
	setUp func()
	once  sync.Once
	// secure says the app set up TLS with the proxy, and handshake when
	// that started and ended, making the certificate it was shown
	// included.
	secure    bool
	handshake [2]time.Time
	// timed is set once a call has carried the handshake time.
	timed atomic.Bool
}

func (a *appConn) Read(p []byte) (int, error) {
	if a.setUp != nil {
		a.once.Do(a.setUp)
	}
	return a.Conn.Read(p)
}

// handshakeTime is when setting up TLS started and ended, for the first
// call on the connection that asks; later calls skipped that step.
func (a *appConn) handshakeTime() (time.Time, time.Time, bool) {
	if a == nil || !a.secure || !a.timed.CompareAndSwap(false, true) {
		return time.Time{}, time.Time{}, false
	}
	return a.handshake[0], a.handshake[1], true
}

// secureAppConn sets up TLS with an app on the proxy's TLS port, timed,
// on its first read. A failed setup never becomes a call: it is a debug
// log line only.
func secureAppConn(raw net.Conn, config *tls.Config, clock Clock, logger *slog.Logger, own *own) *appConn {
	conn := tls.Server(&tlsOnlyConn{Conn: raw}, config)
	a := &appConn{Conn: conn, secure: true}
	a.setUp = func() {
		var err error
		a.handshake, err = timeHandshake(context.Background(), conn, clock)
		if err != nil {
			logger.Debug("app TLS setup failed", "address", raw.RemoteAddr().String(), "reason", err.Error())
			own.tlsFailed(err)
		}
	}
	return a
}

// timeHandshake sets up TLS with an app and says when that started and
// ended.
func timeHandshake(ctx context.Context, conn *tls.Conn, clock Clock) ([2]time.Time, error) {
	start := clock.Now()
	err := conn.HandshakeContext(ctx)
	return [2]time.Time{start, clock.Now()}, err
}

// appConnKey holds, in a call's context, the appConn the call came on.
type appConnKey struct{}

// connContext puts the appConn into the context of every call on it.
func connContext(ctx context.Context, c net.Conn) context.Context {
	if a, ok := c.(*appConn); ok {
		ctx = context.WithValue(ctx, appConnKey{}, a)
	}
	return ctx
}

func appConnOf(r *http.Request) *appConn {
	a, _ := r.Context().Value(appConnKey{}).(*appConn)
	return a
}

// tunnels is the listener the tunnels' HTTP server takes its connections
// from: each tunnel is handed over once TLS inside it is set up, or once
// the app has started talking plain HTTP in it.
type tunnels struct {
	// Listener is nil: tunnels come from every listener, so they have no
	// address of their own, and the HTTP server never asks for one.
	net.Listener
	ready chan net.Conn
	// ctx ends when the proxy stops taking tunnels; tunnels still being
	// set up are cut short.
	ctx    context.Context //nolint:containedctx // the listener's lifetime
	cancel context.CancelFunc
}

func newTunnels() *tunnels {
	ctx, cancel := context.WithCancel(context.Background())
	return &tunnels{ready: make(chan net.Conn), ctx: ctx, cancel: cancel}
}

func (t *tunnels) Accept() (net.Conn, error) {
	select {
	case c := <-t.ready:
		return c, nil
	case <-t.ctx.Done():
		return nil, net.ErrClosed
	}
}

// Close stops taking tunnels. The listeners are closed by their own servers.
func (t *tunnels) Close() error {
	t.cancel()
	return nil
}

// hand gives a tunnel to the HTTP server, or closes it once the proxy has
// stopped taking tunnels.
func (t *tunnels) hand(c net.Conn) {
	select {
	case t.ready <- c:
	case <-t.ctx.Done():
		_ = c.Close()
	}
}
