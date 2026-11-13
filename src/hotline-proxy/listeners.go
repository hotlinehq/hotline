package proxy

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"hotline-proxy/internal/config"
)

// listening is one listener of the config, open and served. Its settings
// change on a reload only while its shape stays the same: a listener that
// moved, or gained or lost a part, keeps its old settings until a restart.
type listening struct {
	name string
	cfg  atomic.Pointer[config.Listener]
	// shape is the shape it was opened with.
	shape    string
	listener net.Listener
	// tls is the certificate shown on a tls listener; nil without tls.
	tls *tlsHolder
	// signer signs the certificates of connect, transparent and capture.
	signer *interceptor
	// server serves a forward or capture listener; a transparent one is
	// served by serveIntercept.
	server *http.Server
	served chan struct{}
}

// listeningKey puts the listener a connection came in on into the context
// of every call on it.
type listeningKey struct{}

func listeningOf(r *http.Request) *listening {
	l, _ := r.Context().Value(listeningKey{}).(*listening)
	return l
}

// openListener opens a listener's port or socket file.
func openListener(ctx context.Context, c *config.Listener) (net.Listener, error) {
	if c.Network == "unix" {
		return listenSocket(ctx, "listener "+c.Name, c.Address)
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", c.Address)
}

// newListening sets up a listener that is already open: its certificates
// and, but for a transparent one, its HTTP server.
func (h *handler) newListening(c *config.Listener, l net.Listener, newServer func(http.Handler) *http.Server) *listening {
	li := &listening{name: c.Name, shape: c.Shape(), listener: l, served: make(chan struct{})}
	li.cfg.Store(c)
	if c.TLS != nil {
		li.tls = newTLSHolder(c.TLS, h.clock, h.logger)
	}
	if issuer := partIssuer(c); issuer != nil {
		li.signer = newInterceptor(issuer, h.clock, h.logger)
	}
	switch {
	case c.Transparent != nil:
	case c.Capture != nil:
		li.server = newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.serveCapture(w, r, li) }))
	default:
		li.server = newServer(h)
		li.server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
			return connContext(context.WithValue(ctx, listeningKey{}, li), conn)
		}
	}
	return li
}

// partIssuer is the issuer that signs for connect, transparent or capture;
// nil when the listener has none of them, or relays with protocol tcp.
func partIssuer(c *config.Listener) *config.TLS {
	switch {
	case c.Connect != nil:
		return c.Connect.Issuer
	case c.Transparent != nil:
		return c.Transparent.Issuer
	case c.Capture != nil:
		return c.Capture.Issuer
	}
	return nil
}

// serve starts taking connections.
func (li *listening) serve(h *handler, where OriginalDestination, o Options) {
	port := li.listener
	if li.tls != nil {
		// The proxy sets up TLS itself, so the server sees plain
		// connections.
		port = tlsOnlyListener{Listener: port, config: li.tls.config(), clock: o.Clock, logger: o.Logger, own: h.own}
	}
	go func() {
		defer close(li.served)
		if li.server == nil {
			h.serveIntercept(port, where, li)
			return
		}
		_ = li.server.Serve(port)
	}()
}

// shutdown stops taking connections and lets calls in flight finish, as
// long as ctx allows.
func (li *listening) shutdown(ctx context.Context) error {
	var err error
	if li.server == nil {
		_ = li.listener.Close()
	} else {
		err = li.server.Shutdown(ctx)
	}
	<-li.served
	return err
}

// reload takes the settings of a new config, when the listener keeps its
// shape there.
func (li *listening) reload(c *config.Listener) {
	if c == nil || c.Shape() != li.shape {
		return
	}
	li.cfg.Store(c)
	if li.tls != nil {
		li.tls.set(c.TLS)
	}
	if issuer := partIssuer(c); issuer != nil {
		li.signer.issuer.set(issuer)
	}
}

// tlsHolder is the certificate a listener or the admin port shows apps:
// one shown as it is, or a certificate signed for the name the app calls.
type tlsHolder struct {
	shown  *certHolder
	signer *interceptor
}

func newTLSHolder(t *config.ListenerTLS, clock Clock, logger *slog.Logger) *tlsHolder {
	if t.Sign {
		return &tlsHolder{signer: newInterceptor(t.Cert, clock, logger)}
	}
	return &tlsHolder{shown: newAppCert(t.Cert, logger)}
}

// config is the TLS setup: TLS 1.2 and 1.3, offering HTTP/2 and HTTP/1.1.
// A signed certificate is for the name the app asks for, or for the address
// it called when it names none.
func (t *tlsHolder) config() *tls.Config {
	if t.shown != nil {
		return t.shown.tlsConfig()
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name, _, _ = net.SplitHostPort(hello.Conn.LocalAddr().String())
			}
			if name == "" {
				name = "localhost"
			}
			return t.signer.leaf(name)
		},
	}
}

// set puts in the certificate of a new config.
func (t *tlsHolder) set(c *config.ListenerTLS) {
	if t.shown != nil {
		t.shown.set(c.Cert)
		return
	}
	t.signer.issuer.set(c.Cert)
}

// notAfter is when the certificate shown, or the one signing, expires.
func (t *tlsHolder) notAfter() time.Time {
	if t.shown != nil {
		return t.shown.get().Leaf.NotAfter
	}
	return t.signer.issuer.get().Leaf.NotAfter
}

// newHTTPServer is the HTTP server of a forward or capture listener, and of
// the tunnels.
func newHTTPServer(handler http.Handler, logger *slog.Logger) *http.Server {
	// HTTP/1.1, and HTTP/2 from the first byte (prior knowledge), also
	// after TLS, which the proxy sets up itself. The older Upgrade: h2c
	// handshake is not offered.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          log.New(serverLog{logger: logger}, "", 0),
		ConnContext:       connContext,
	}
}

// certificates are when each certificate of the proxy's own expires, by
// where it is used: the listener's name and part, or admin.tls.
func (p *Proxy) certificates() map[string]time.Time {
	at := map[string]time.Time{}
	if p.adminTLS != nil {
		at["admin.tls"] = p.adminTLS.notAfter()
	}
	for _, li := range p.listeners {
		c := li.cfg.Load()
		if li.tls != nil {
			at[li.name+".tls"] = li.tls.notAfter()
		}
		if li.signer != nil {
			at[li.name+"."+part(c)] = li.signer.issuer.get().Leaf.NotAfter
		}
	}
	return at
}

// part names the part of a listener that signs: connect, transparent or
// capture.
func part(c *config.Listener) string {
	switch {
	case c.Connect != nil:
		return "connect"
	case c.Transparent != nil:
		return "transparent"
	}
	return "capture"
}
