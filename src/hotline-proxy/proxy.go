// Package proxy is the Hotline proxy: it forwards each call from an app to
// its vendor, measures it, and sends one record per call over OTLP.
package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"expvar"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof" //nolint:gosec // only served when telemetry.pprof is on
	"net/netip"
	"runtime"
	"sync/atomic"
	"time"

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
	// Console is where a console exporter prints: standard output, and
	// whether it is a terminal. Left empty, a console exporter prints
	// nothing.
	Console Console
}

// Console is standard output, for a console exporter, and the width of
// the terminal it is, or false when it is not a terminal.
type Console struct {
	Out      io.Writer
	Terminal func() (int, bool)
	// JSON makes the console exporter print each call as one line of
	// JSON, as for --format json.
	JSON bool
}

// export is the console as the console exporter takes it. A program that
// starts the proxy without saying where the console is gets one that is no
// terminal and prints nothing, so a console exporter in its config cannot
// stop the proxy.
func (c Console) export(lookupEnv func(string) (string, bool)) export.Console {
	out := export.Console{Out: c.Out, Terminal: c.Terminal, LookupEnv: lookupEnv, JSON: c.JSON}
	if out.Out == nil {
		out.Out = io.Discard
	}
	if out.Terminal == nil {
		out.Terminal = func() (int, bool) { return 0, false }
	}
	return out
}

// Proxy is a running proxy.
type Proxy struct {
	// listeners are the config's listeners, open and served, by name.
	listeners []*listening
	// tunnelServer serves the calls inside tunnels opened by CONNECT.
	tunnelServer *http.Server
	handler      *handler
	transports   []*http.Transport
	sink         *export.Sink
	// telemetry sends the proxy's own numbers.
	telemetry    *export.Own
	tunnelServed chan struct{}
	// admin serves the health checks, when the config names an admin
	// address, and adminTLS is its certificate when it takes TLS.
	admin       *http.Server
	adminServed chan struct{}
	adminTLS    *tlsHolder
	// stopping is set from the first stop signal on, so /readyz says no.
	stopping atomic.Bool
	reload   *reloader
}

// Start loads the config, opens its listeners and serves calls until
// Shutdown. A config with problems is refused with all of them listed.
func Start(ctx context.Context, o Options) (*Proxy, error) {
	cfg, err := config.Load(o.ConfigPath, o.Clock.Now(), o.LookupEnv)
	if err != nil {
		return nil, err
	}
	opened := make([]net.Listener, 0, len(cfg.Listeners))
	for _, c := range cfg.Listeners {
		l, err := openListener(ctx, c)
		if err != nil {
			closeAll(opened...)
			return nil, err
		}
		opened = append(opened, l)
	}
	// The admin port listens only once the config is loaded and the
	// listeners listen, so until then Kubernetes finds the proxy not ready.
	var adminListener net.Listener
	if cfg.Admin != nil {
		adminListener, err = openListener(ctx, &config.Listener{Name: "admin", Network: cfg.Admin.Network, Address: cfg.Admin.Address})
		if err != nil {
			closeAll(opened...)
			return nil, err
		}
	}
	service := export.Service{Version: o.Version, InstanceID: o.InstanceID}
	timing := export.Timing{Timeout: o.ExportTimeout}
	telemetry := export.NewOwn(cfg.Telemetry, o.RootCAs, service, timing)
	sink, err := export.New(cfg.Exporters, cfg.FileExporters, cfg.Console,
		o.Console.export(o.LookupEnv), o.RootCAs, service, timing, o.Logger, telemetry.Meter())
	if err != nil {
		closeAll(append(opened, adminListener)...)
		_ = telemetry.Shutdown(ctx)
		return nil, err
	}
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
	// Transparent and connect listeners relay passthrough sites.
	h.relays = &dialer{resolver: o.Resolver, dialer: o.Dialer, clock: o.Clock}
	// Calls that may reach internal IPs get their own connections, so a
	// connection opened for them is never reused for a call that may not.
	h.transport = newTransport(o, false)
	h.internal = newTransport(o, true)
	h.tunnels = newTunnels()
	newServer := func(handler http.Handler) *http.Server { return newHTTPServer(handler, o.Logger) }
	p := &Proxy{
		handler:      h,
		tunnelServer: newServer(h),
		transports:   []*http.Transport{h.transport, h.internal},
		sink:         sink,
		telemetry:    telemetry,
		tunnelServed: make(chan struct{}),
	}
	for i, c := range cfg.Listeners {
		p.listeners = append(p.listeners, h.newListening(c, opened[i], newServer))
	}
	p.reload = &reloader{path: o.ConfigPath, clock: o.Clock, lookupEnv: o.LookupEnv, logger: o.Logger, own: h.own, started: cfg}
	h.own.observe(p)
	p.reload.seen = stat(cfg.Files)
	go func() {
		defer close(p.tunnelServed)
		_ = p.tunnelServer.Serve(h.tunnels)
	}()
	addresses := make([]any, 0, 2*len(cfg.Listeners))
	for _, li := range p.listeners {
		li.serve(h, o.OriginalDestination, o)
		addresses = append(addresses, "listener."+li.name, li.listener.Addr().String())
	}
	if adminListener != nil {
		if cfg.Admin.TLS != nil {
			p.adminTLS = newTLSHolder(cfg.Admin.TLS, o.Clock, o.Logger)
			adminListener = tls.NewListener(adminListener, p.adminTLS.config())
		}
		p.serveAdmin(adminListener, o.Logger, cfg.Pprof)
	}
	for _, w := range cfg.Warnings {
		o.Logger.Warn("config warning", "reason", w)
	}
	o.Logger.Info("hotline proxy started", append(addresses, "config_version", cfg.Label())...)
	return p, nil
}

// closeAll closes the listeners opened so far, when the proxy cannot start.
func closeAll(listeners ...net.Listener) {
	for _, l := range listeners {
		if l != nil {
			_ = l.Close()
		}
	}
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
	// Every server stops at once: a tunnel opened over HTTP/2 is a call on
	// a listener that lasts until the tunnels' server closes the tunnel.
	// Tunnels still being set up are cut short.
	stopped := make(chan error, len(p.listeners))
	for _, li := range p.listeners {
		go func() { stopped <- li.shutdown(ctx) }()
	}
	err := p.tunnelServer.Shutdown(ctx)
	for range p.listeners {
		err = errors.Join(err, <-stopped)
	}
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
