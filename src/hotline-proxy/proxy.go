// Package proxy is the Hotline proxy: it forwards each call from an app to
// its vendor, measures it, and sends one record per call over OTLP.
package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"expvar"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof" //nolint:gosec // only served when telemetry.pprof is on
	"net/netip"
	"runtime"
	"sync/atomic"
	"time"

	"hotline-proxy/internal/capture"
	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
)

// Resolver looks up the addresses of a vendor host. *net.Resolver is one.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer opens connections to vendors. *net.Dialer is one.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Clock tells the time and waits. SystemClock is the real one.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a wait started by Clock.AfterFunc.
type Timer interface {
	Stop() bool
}

// SystemClock is the computer's own clock.
type SystemClock struct{}

// Now is time.Now.
func (SystemClock) Now() time.Time { return time.Now() }

// AfterFunc is time.AfterFunc.
func (SystemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Options hold what the proxy needs from the world. Every field is required.
type Options struct {
	// ConfigPath is the hotline-proxy.yaml file.
	ConfigPath string
	Resolver   Resolver
	Dialer     Dialer
	// RootCAs are the trusted certificate issuers, for vendors and for
	// collectors over TLS: the system's own and any extra ones.
	RootCAs *x509.CertPool
	Clock   Clock
	Logger  *slog.Logger
	// ExportTimeout is how long one send to a destination may take,
	// retries included.
	ExportTimeout time.Duration
	Version       string
	InstanceID    string
	// LookupEnv reads the environment, for secrets the config reads from
	// it.
	LookupEnv func(string) (string, bool)
	// CheckWorkers is how many spec checks run at once: by default one
	// per processor core the proxy may use.
	CheckWorkers int
	// OriginalDestination reads where a connection on listen.intercept was
	// first sent, before the firewall redirected it. Without it, calls
	// that name no site cannot be intercepted.
	OriginalDestination OriginalDestination
}

// Proxy is a running proxy.
type Proxy struct {
	server *http.Server
	// tunnelServer serves the calls inside tunnels opened by CONNECT.
	tunnelServer *http.Server
	handler      *handler
	transports   []*http.Transport
	sink         *export.Sink
	// telemetry sends the proxy's own numbers.
	telemetry    *export.Own
	served       chan struct{}
	tunnelServed chan struct{}
	// admin serves the health checks, when the config names an admin
	// address.
	admin       *http.Server
	adminServed chan struct{}
	// stopping is set from the first stop signal on, so /readyz says no.
	stopping atomic.Bool
	// appCert is the certificate of the proxy port, when it takes TLS.
	appCert *certHolder
	reload  *reloader
	// redirectedListener takes the connections of listen.intercept, when
	// the config has it.
	redirectedListener net.Listener
	redirectedServed   chan struct{}
	// captureFolder, captureListener and captureHub are the socket of
	// listen.capture, when the config has it: its private folder, the
	// listener and the extension's control connections.
	captureFolder   *capture.Folder
	captureListener net.Listener
	captureHub      *captureHub
	captureServed   chan struct{}
}

// CaptureSocket is the path of the socket the macOS extension connects to,
// which the proxy makes when the config has listen.capture; empty without.
func (p *Proxy) CaptureSocket() string {
	if p.captureFolder == nil {
		return ""
	}
	return p.captureFolder.Socket()
}

// Start loads the config, listens on its proxy address and serves calls
// until Shutdown. A config with problems is refused with all of them listed.
// With listen.tls set, the proxy port accepts only TLS. With listen.connect
// set, CONNECT is an ingress.
func Start(ctx context.Context, o Options) (*Proxy, error) {
	cfg, err := config.Load(o.ConfigPath, o.Clock.Now(), o.LookupEnv)
	if err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ProxyAddr)
	if err != nil {
		return nil, err
	}
	// The admin port listens only once the config is loaded and the proxy
	// port listens, so until then Kubernetes finds the proxy not ready.
	var adminListener net.Listener
	if cfg.AdminAddr != "" {
		adminListener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.AdminAddr)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	var redirectedListener net.Listener
	if cfg.Intercept != nil {
		redirectedListener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Intercept.Address)
		if err != nil {
			_ = listener.Close()
			if adminListener != nil {
				_ = adminListener.Close()
			}
			return nil, err
		}
	}
	var captureFolder *capture.Folder
	var captureListener net.Listener
	if cfg.Capture != nil {
		captureFolder, captureListener, err = listenCapture(ctx)
		if err != nil {
			_ = listener.Close()
			if adminListener != nil {
				_ = adminListener.Close()
			}
			if redirectedListener != nil {
				_ = redirectedListener.Close()
			}
			return nil, err
		}
	}
	service := export.Service{Version: o.Version, InstanceID: o.InstanceID}
	timing := export.Timing{Timeout: o.ExportTimeout}
	telemetry := export.NewOwn(cfg.Telemetry, o.RootCAs, service, timing)
	sink := export.New(cfg.Exporters, o.RootCAs, service, timing, o.Logger, telemetry.Meter())
	h := &handler{
		clock:  o.Clock,
		logger: o.Logger,
		sink:   sink,
		own:    newOwn(telemetry.Meter()),
	}
	h.forward.Buffers = &h.buffers
	h.cfg.Store(cfg)
	workers := o.CheckWorkers
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	h.checks = newCheckLine(workers, cfg.CheckQueueMemory, o.Clock, h.own.checkDropped)
	if cfg.Connect != nil {
		h.intercept = newInterceptor(cfg.Connect, o.Clock, o.Logger)
	}
	if cfg.Capture != nil {
		h.captureCerts = newInterceptor(cfg.Capture.Issuer, o.Clock, o.Logger)
	}
	if redirectedListener != nil {
		h.redirected = newInterceptor(cfg.Intercept.Issuer, o.Clock, o.Logger)
	}
	if redirectedListener != nil || cfg.Capture != nil {
		h.relays = &dialer{resolver: o.Resolver, dialer: o.Dialer, clock: o.Clock}
	}
	// Calls that may reach internal IPs get their own connections, so a
	// connection opened for them is never reused for a call that may not.
	h.transport = newTransport(o, false)
	h.internal = newTransport(o, true)
	// HTTP/1.1, and HTTP/2 from the first byte (prior knowledge), also
	// after TLS, which the proxy sets up itself. The older Upgrade: h2c
	// handshake is not offered.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	h.tunnels = newTunnels(listener)
	newServer := func() *http.Server {
		return &http.Server{
			Handler:           h,
			Protocols:         protocols,
			ReadHeaderTimeout: 30 * time.Second,
			ErrorLog:          log.New(serverLog{logger: o.Logger}, "", 0),
			ConnContext:       connContext,
		}
	}
	p := &Proxy{
		handler:      h,
		server:       newServer(),
		tunnelServer: newServer(),
		transports:   []*http.Transport{h.transport, h.internal},
		sink:         sink,
		telemetry:    telemetry,
		served:       make(chan struct{}),
		tunnelServed: make(chan struct{}),
	}
	port := listener
	if cfg.TLS != nil {
		// The proxy sets up TLS itself, so the server sees plain
		// connections.
		p.appCert = newAppCert(cfg.TLS, o.Logger)
		port = tlsOnlyListener{Listener: listener, config: p.appCert.tlsConfig(), clock: o.Clock, logger: o.Logger, own: h.own}
	}
	p.reload = &reloader{path: o.ConfigPath, clock: o.Clock, lookupEnv: o.LookupEnv, logger: o.Logger, own: h.own, started: cfg}
	h.own.observe(p)
	p.reload.seen = stat(cfg.Files)
	go func() {
		defer close(p.served)
		_ = p.server.Serve(port)
	}()
	go func() {
		defer close(p.tunnelServed)
		_ = p.tunnelServer.Serve(h.tunnels)
	}()
	if redirectedListener != nil {
		p.redirectedListener, p.redirectedServed = redirectedListener, make(chan struct{})
		go func() {
			defer close(p.redirectedServed)
			h.serveIntercept(redirectedListener, o.OriginalDestination)
		}()
	}
	if captureListener != nil {
		p.captureFolder, p.captureListener, p.captureServed = captureFolder, captureListener, make(chan struct{})
		p.captureHub = newCaptureHub(cfg.Capture.Apps)
		go func() {
			defer close(p.captureServed)
			h.serveCapture(captureListener, p.captureHub)
		}()
	}
	if adminListener != nil {
		p.serveAdmin(adminListener, o.Logger, cfg.Pprof)
	}
	for _, w := range cfg.Warnings {
		o.Logger.Warn("config warning", "reason", w)
	}
	o.Logger.Info("hotline proxy started", "address", listener.Addr().String(), "config_version", cfg.Label())
	return p, nil
}

// listenCapture makes the private folder of listen.capture and listens on a
// socket in it.
func listenCapture(ctx context.Context) (*capture.Folder, net.Listener, error) {
	folder, err := capture.NewFolder()
	if err != nil {
		return nil, nil, err
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "unix", folder.Socket())
	if err != nil {
		_ = folder.Close()
		return nil, nil, err
	}
	return folder, l, nil
}

func newTransport(o Options, allowInternal bool) *http.Transport {
	d := &dialer{resolver: o.Resolver, dialer: o.Dialer, clock: o.Clock, allowInternal: allowInternal}
	return &http.Transport{
		DialContext:         d.dial,
		TLSClientConfig:     &tls.Config{RootCAs: o.RootCAs, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
}

// Validate checks a config and every spec and certificate it points to,
// without starting anything. A config with problems is refused with all of
// them listed. A valid config may still come with warnings, as at start.
func Validate(configPath string, clock Clock, lookupEnv func(string) (string, bool)) ([]string, error) {
	cfg, err := config.Load(configPath, clock.Now(), lookupEnv)
	if err != nil {
		return nil, err
	}
	return cfg.Warnings, nil
}

// serveAdmin answers the health checks: /livez says the proxy is alive
// while it answers at all, /readyz says it takes calls until it starts to
// stop. Nothing else is served there, and nothing there is recorded, except
// Go's profile pages when telemetry.pprof is on.
func (p *Proxy) serveAdmin(l net.Listener, logger *slog.Logger, pprofPages bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if p.stopping.Load() {
			http.Error(w, "not ready: stopping", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	if pprofPages {
		// Off unless telemetry.pprof is set: the admin port has no password.
		logger.Warn("telemetry.pprof is on: Go's profile pages are served on the admin port, keep it on a private network")
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
		mux.Handle("GET /debug/vars", expvar.Handler())
	}
	p.admin = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(serverLog{logger: logger}, "", 0),
	}
	p.adminServed = make(chan struct{})
	go func() {
		defer close(p.adminServed)
		_ = p.admin.Serve(l)
	}()
}

// Drain makes /readyz say no, so Kubernetes stops sending new calls, while
// the proxy still takes the calls that come.
func (p *Proxy) Drain() {
	p.stopping.Store(true)
}

// Shutdown stops taking calls, lets calls in flight finish, then sends every
// queued record, all within the time ctx allows. The health checks answer
// until the end, /readyz with no.
func (p *Proxy) Shutdown(ctx context.Context) error {
	p.Drain()
	// Both servers stop at once: a tunnel opened over HTTP/2 is a call on
	// the proxy port that lasts until the tunnels' server closes the
	// tunnel. Tunnels still being set up are cut short.
	if p.redirectedListener != nil {
		_ = p.redirectedListener.Close()
		<-p.redirectedServed
	}
	if p.captureListener != nil {
		_ = p.captureListener.Close()
		<-p.captureServed
		p.captureHub.close()
		_ = p.captureFolder.Close()
	}
	stopped := make(chan error, 1)
	go func() { stopped <- p.server.Shutdown(ctx) }()
	err := errors.Join(p.tunnelServer.Shutdown(ctx), <-stopped)
	<-p.served
	<-p.tunnelServed
	finished := make(chan struct{})
	go func() {
		p.handler.calls.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		p.handler.checks.stop(false)
	case <-ctx.Done():
		// Checks still waiting are skipped, so their calls hand over their
		// records now.
		skipped := p.handler.checks.stop(true)
		p.handler.logger.Warn("stop ran out of time, skipped the spec checks still waiting", "skipped", skipped)
	}
	for _, t := range p.transports {
		t.CloseIdleConnections()
	}
	// The proxy's own numbers go last, so they count the final sends.
	err = errors.Join(err, p.sink.Shutdown(ctx), p.telemetry.Shutdown(ctx))
	if p.admin != nil {
		_ = p.admin.Close()
		<-p.adminServed
	}
	return err
}
