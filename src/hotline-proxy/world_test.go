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
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	proxy "hotline-proxy"
)

var (
	errNoRecord = errors.New("no record arrived")
	// errTLSSetup is a TLS connection to the proxy that failed to set up;
	// world.tlsErr says why.
	errTLSSetup = errors.New("TLS to the proxy failed to set up")
)

// world is one scenario's system under test: the proxy, and the fakes at
// its edges (vendors, network, OTLP receivers). Every scenario starts clean.
type world struct {
	// checkWorkers is how many spec checks run at once; 0 is one per core.
	checkWorkers int
	// topExtra is added to the config's top level.
	// stoppedEarly says the proxy was stopped with no time left, which cuts off
	// the exporters' sends.
	stoppedEarly bool
	topExtra     string
	// lastSent are the headers of the app's last call.
	lastSent []string
	dir      string
	issuer   *issuer
	network  *network
	vendors  map[string]*vendor
	// receivers are the OTLP destinations, in the order of the config.
	receivers []*receiver
	// telemetry, when set, is the destination for the proxy's own health
	// data.
	telemetry *receiver
	// telemetryDisable are the metrics the telemetry section turns off.
	telemetryDisable []string
	// lastMetric is the proxy's own metric a scenario last looked at.
	lastMetric string
	// callDone gets the result of a call started in the background.
	callDone chan error
	// downEndpoints are exporter endpoints where nothing listens.
	downEndpoints []string
	// exporterExtra are more lines for the exporter of each receiver.
	exporterExtra []string
	// files are the file exporters: each one's folder and more lines.
	files []fileExporter
	// sentBody is the body of the app's last call, when it sent one.
	sentBody string
	// unsetIntervals leaves the intervals out of the exporters, and
	// unsetOwnInterval out of the telemetry section, so the defaults apply.
	// Otherwise everything is sent every 20ms, to keep tests quick.
	unsetIntervals   bool
	unsetOwnInterval bool
	// telemetryPprof turns on the profile pages in the telemetry section.
	telemetryPprof bool
	// telemetryExtra are more lines for the telemetry section's otlp.
	telemetryExtra string
	// env is the proxy's environment.
	env map[string]string
	// processEnv are variables set in the test process itself, unset with
	// the world.
	processEnv []string

	// apis are the vendor apis of the config's apis section.
	apis []namedAPI
	// integrations is the YAML of the config's integrations list.
	integrations []string
	// noIntegrations leaves the integrations out of the config; otherwise
	// a config with none gets talon-shop-a for Talon.One and Stripe.
	noIntegrations bool
	// rawConfig, when set, replaces the generated config file.
	rawConfig string
	// adminAddr is the admin listen address; any free port when empty.
	adminAddr string
	// busy is the address another program already listens on.
	busy       string
	configPath string
	// configName is the config file's name; empty means hotline-proxy.yaml.
	configName string

	proxy    *proxy.Proxy
	addr     string
	startErr error
	logs     *syncBuffer

	// clock, when set, replaces the system clock, so waits end only when
	// the test lets time pass.
	clock *fakeClock

	// appID is the integration ID apps send; empty sends none.
	appID string
	// method and target are the call apps make unless a step says
	// otherwise.
	method string
	target string
	// missing are hosts the fake DNS does not know.
	missing map[string]bool

	stream    *stream
	listeners []net.Listener

	// problem is what a refused config's message must name.
	problem string
	// parameter is the spec parameter a scenario is about.
	parameter string
	// freed, when closed, lets the first of two calls end.
	freed chan struct{}
	// quiet, when closed, lets a quiet vendor send the rest of its reply.
	quiet chan struct{}
	// cutOff is closed when the proxy stops waiting for a quiet vendor.
	cutOff chan struct{}
	// exportTimeout is how long one send may take; 2s unless set, so a slow
	// first connection under the race detector still gets through.
	exportTimeout time.Duration
	// pending is a call a Given arranged for a later When.
	pending *request
	appTook time.Duration

	resp     *http.Response
	respBody []byte
	respErr  error

	// listenTLS, when set, is the listen.tls line of the config.
	listenTLS string
	// proxyCert is the certificate the proxy's cert and key files hold.
	proxyCert tls.Certificate
	// appTLS, when set, makes apps open TLS to the proxy with it.
	appTLS *tls.Config
	// tlsState is what the app's last TLS connection agreed on, and
	// tlsErr why setting it up failed.
	tlsState tls.ConnectionState
	tlsErr   error
	// openConn is an app connection kept open between steps.
	openConn net.Conn
	// renewed is the certificate that replaced proxyCert in its files.
	renewed *tls.Certificate

	// listenConnect, when set, is the listen.connect line of the config.
	listenConnect string
	// issuersSection is the config's issuers section, when it has one.
	issuersSection string
	// interception is the issuer in the listen.connect files, and second
	// the one that replaced it.
	interception *issuer
	second       *issuer
	// tunnelRoots are the issuers an app trusts inside a tunnel, and
	// tunnelProtos the HTTP versions it offers there; none means both.
	tunnelRoots  *x509.CertPool
	tunnelProtos []string
	// tunnel is the last tunnel opened, and tunnels all of them, closed
	// with the world. tunnelErr says why TLS inside the last one failed.
	tunnel  net.Conn
	tunnels []net.Conn
	// h2 is the app's HTTP/2 connection for tunnels, and h2Reused says
	// the last tunnel shared it.
	h2        *http.Transport
	h2Reused  bool
	tunnelErr error
	// tunnelCerts are the certificates the proxy showed in each tunnel.
	tunnelCerts []*x509.Certificate
	// intercept is the listen.intercept setting, when the scenario has one.
	intercept *interceptConf
	// interceptHost is the site the app's last connection to the intercept
	// listener named, and original what the connection was first sent to,
	// when the scenario lets the proxy read that.
	interceptHost string
	interceptAddr string
	original      netip.AddrPort
	originalKnown bool
	// sentRecords are the records that arrived while the proxy runs.
	sentRecords []receivedRecord
}

var errNoOriginal = errors.New("the original destination is not known")

func newWorld() (*world, error) {
	dir, err := os.MkdirTemp("", "hotline-proxy-test-")
	if err != nil {
		return nil, err
	}
	return &world{
		dir:       dir,
		issuer:    newIssuer(),
		network:   newNetwork(),
		vendors:   map[string]*vendor{},
		receivers: []*receiver{newReceiver()},
		logs:      &syncBuffer{},
		appID:     "talon-shop-a",
		method:    "PUT",
		target:    "https://api.talon.one/v2/customer_sessions/sess-8812?dry=true",
		missing:   map[string]bool{},
		stream:    &stream{},
	}, nil
}

func (w *world) close() {
	for _, c := range w.tunnels {
		_ = c.Close()
	}
	if w.h2 != nil {
		w.h2.CloseIdleConnections()
	}
	if w.proxy != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = w.proxy.Shutdown(ctx)
		cancel()
	}
	for _, v := range w.vendors {
		v.close()
	}
	for _, r := range append(w.receivers, w.telemetry) {
		if r != nil {
			r.stop()
		}
	}
	for _, l := range w.listeners {
		_ = l.Close()
	}
	if w.openConn != nil {
		_ = w.openConn.Close()
	}
	for _, name := range w.processEnv {
		_ = os.Unsetenv(name)
	}
	_ = os.RemoveAll(w.dir)
}

// vendor returns the fake vendor for host, starting it on first use.
func (w *world) vendor(host string) *vendor {
	if v, ok := w.vendors[host]; ok {
		return v
	}
	v := &vendor{host: host, ip: ipFor(len(w.vendors) + 10)}
	w.vendors[host] = v
	w.network.name(host, v.ip)
	v.notAfter = time.Now().Add(24 * time.Hour).Truncate(time.Second)
	cert := w.issuer.leaf(host, v.notAfter, false)
	for _, port := range []string{"443", "8443", "8080"} {
		s := httptest.NewUnstartedServer(v.record("https", port))
		s.EnableHTTP2 = true
		s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		s.StartTLS()
		v.servers = append(v.servers, s)
		w.network.route(net.JoinHostPort(v.ip.String(), port), s.Listener.Addr().String())
	}
	// Port 80 answers plain http and, since the proxy keeps the app's port
	// when it turns a call into https, also https.
	port80 := sharedPort()
	plain := httptest.NewUnstartedServer(v.record("http", "80"))
	plain.Listener = port80.plain
	plain.Start()
	secure := httptest.NewUnstartedServer(v.record("https", "80"))
	secure.Listener = port80.tls
	secure.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	secure.StartTLS()
	v.servers = append(v.servers, plain, secure)
	w.network.route(net.JoinHostPort(v.ip.String(), "80"), port80.addr)
	return v
}

// replaceTLS swaps the certificate a vendor shows on https port 443.
func (w *world) replaceTLS(host string, cert tls.Certificate) {
	v := w.vendor(host)
	s := httptest.NewUnstartedServer(v.record("https", "443"))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	s.StartTLS()
	v.servers = append(v.servers, s)
	w.network.route(net.JoinHostPort(v.ip.String(), "443"), s.Listener.Addr().String())
}

// route sends an address of the fake network to a local listener.
func (w *world) routeTo(host, port string, listener net.Listener) {
	v := w.vendor(host)
	w.network.route(net.JoinHostPort(v.ip.String(), port), listener.Addr().String())
}

func (w *world) writeFile(name, content string) string {
	path := filepath.Join(w.dir, name)
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	_ = os.WriteFile(path, []byte(content), 0o600) //nolint:gosec // the test chooses its own files
	return path
}

func (w *world) exampleFile(name string) string {
	data, _ := os.ReadFile(filepath.Join("testdata", "proxy-example", name)) //nolint:gosec // test data
	return string(data)
}

// addIntegration adds one integration, written as YAML, to the config.
func (w *world) addIntegration(yaml string) {
	w.integrations = append(w.integrations, yaml)
}

// namedAPI is one entry of the config's apis section.
type namedAPI struct {
	name string
	yaml string
}

// api adds a vendor api, written as YAML, to the config's apis section and
// returns the line an integration uses to name it. The same YAML is added
// only once.
func (w *world) api(name, yaml string) string {
	taken := map[string]bool{}
	for _, a := range w.apis {
		if a.yaml == yaml {
			return "      - " + a.name + "\n"
		}
		taken[a.name] = true
	}
	base := name
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	w.apis = append(w.apis, namedAPI{name: name, yaml: yaml})
	return "      - " + name + "\n"
}

// talonAPI adds an api for api.talon.one with the Talon.One spec.
func (w *world) talonAPI(extra string) string {
	w.writeFile("specs/talon-one.openapi.yaml", w.exampleFile("specs/talon-one.openapi.yaml"))
	return w.api("talon-one", "    hosts: [api.talon.one]\n    spec: ./specs/talon-one.openapi.yaml\n"+extra)
}

func (w *world) stripeAPI(extra string) string {
	w.writeFile("specs/stripe.openapi.yaml", w.exampleFile("specs/stripe.openapi.yaml"))
	return w.api("stripe", "    hosts: [api.stripe.com]\n    spec: ./specs/stripe.openapi.yaml\n"+extra)
}

// specAPI adds an api for host with a spec written by the test.
func (w *world) specAPI(host, spec, extra string) string {
	name := fmt.Sprintf("specs/%s.yaml", host)
	w.writeFile(name, spec)
	return w.api(host, fmt.Sprintf("    hosts: [%s]\n    spec: ./%s\n%s", host, name, extra))
}

func integration(id string, apis ...string) string {
	return fmt.Sprintf("  - id: %s\n    apis:\n%s", id, strings.Join(apis, ""))
}

// apisSection is the YAML of the config's apis section.
func (w *world) apisSection() string {
	if len(w.apis) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("apis:\n")
	for _, a := range w.apis {
		b.WriteString("  " + a.name + ":\n" + a.yaml)
	}
	return b.String()
}

// raw is a config written by a test: its head, the apis added so far and
// the integrations.
func (w *world) raw(head, integrations string) string {
	return head + w.apisSection() + "integrations:\n" + integrations
}

func freePort() string {
	l := listenLocal()
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func (w *world) config() string {
	var b strings.Builder
	admin := w.adminAddr
	if admin == "" {
		admin = "127.0.0.1:0"
	}
	fmt.Fprintf(&b, "listen:\n  proxy: %q\n  admin: %q\n", w.addr, admin)
	b.WriteString(w.listenTLS)
	b.WriteString(w.listenConnect)
	b.WriteString(w.interceptSection())
	b.WriteString(w.issuersSection)
	b.WriteString("exporters:\n")
	for i, r := range w.receivers {
		b.WriteString("  - otlp:\n")
		fmt.Fprintf(&b, "      endpoint: %s\n", r.endpoint)
		extra := ""
		if i < len(w.exporterExtra) {
			extra = w.exporterExtra[i]
		}
		b.WriteString(extra)
		for _, name := range []string{"metricInterval", "logInterval", "traceInterval"} {
			if !w.unsetIntervals && !strings.Contains(extra, name) {
				b.WriteString("      " + name + ": 20ms\n")
			}
		}
	}
	for _, e := range w.downEndpoints {
		fmt.Fprintf(&b, "  - otlp:\n      endpoint: %s\n", e)
	}
	for _, f := range w.files {
		if f.raw != "" {
			b.WriteString(f.raw)
			continue
		}
		fmt.Fprintf(&b, "  - file:\n      path: %s\n%s", f.path, f.extra)
	}
	if w.telemetry == nil && w.telemetryPprof {
		b.WriteString("telemetry:\n  pprof: true\n")
	}
	if w.telemetry != nil {
		fmt.Fprintf(&b, "telemetry:\n  otlp:\n    endpoint: %s\n", w.telemetry.endpoint)
		if !w.unsetOwnInterval {
			b.WriteString("    metricInterval: 20ms\n")
		}
		b.WriteString(w.telemetryExtra)
		for i, name := range w.telemetryDisable {
			if i == 0 {
				b.WriteString("  disable:\n")
			}
			fmt.Fprintf(&b, "    - %q\n", name)
		}
		if w.telemetryPprof {
			b.WriteString("  pprof: true\n")
		}
	}
	b.WriteString(w.topExtra)
	b.WriteString(w.apisSection())
	if len(w.integrations) > 0 {
		b.WriteString("integrations:\n" + strings.Join(w.integrations, ""))
	}
	return b.String()
}

// start writes the config and starts the proxy, if not done yet.
func (w *world) start() error {
	if w.proxy != nil || w.startErr != nil {
		return w.startErr
	}
	if w.addr == "" {
		w.addr = freePort()
	}
	if w.intercept != nil && w.intercept.address == "" && w.intercept.without != "address" {
		// Picked as late as possible, so no other scenario takes it first.
		w.intercept.address = freePort()
		w.interceptAddr = w.intercept.address
	}
	if len(w.integrations) == 0 && !w.noIntegrations {
		w.addIntegration(integration("talon-shop-a", w.talonAPI(""), w.stripeAPI("")))
	}
	if w.network.stuck == nil {
		w.network.stuck = w.stuck
	}
	if w.configPath == "" {
		content := w.rawConfig
		if content == "" {
			content = w.config()
		}
		name := w.configName
		if name == "" {
			name = "hotline-proxy.yaml"
		}
		w.configPath = w.writeFile(name, content)
	}
	if w.exportTimeout == 0 {
		w.exportTimeout = 2 * time.Second
	}
	var clock proxy.Clock = proxy.SystemClock{}
	if w.clock != nil {
		clock = w.clock
	}
	w.proxy, w.startErr = proxy.Start(context.Background(), proxy.Options{
		ConfigPath:    w.configPath,
		Resolver:      w.network,
		Dialer:        w.network,
		RootCAs:       w.issuer.pool,
		Clock:         clock,
		Logger:        slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ExportTimeout: w.exportTimeout,
		CheckWorkers:  w.checkWorkers,
		OriginalDestination: func(net.Conn) (netip.AddrPort, error) {
			if !w.originalKnown {
				return netip.AddrPort{}, errNoOriginal
			}
			return w.original, nil
		},
		Version:    "1.0.0-test",
		InstanceID: "proxy-test-1",
		LookupEnv: func(name string) (string, bool) {
			v, ok := w.env[name]
			return v, ok
		},
	})
	return w.startErr
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
func (w *world) defaultCall(headers ...string) error {
	return w.call(w.method, w.target, headers...)
}

// call sends a request the way an app does, with the world's integration ID.
func (w *world) call(method, target string, headers ...string) error {
	if w.appID != "" {
		headers = append([]string{basic(w.appID + ":x")}, headers...)
	}
	return w.send(request{method: method, target: target, headers: headers, length: -1})
}

// stuck lets an hour pass when the proxy waits on a fake that never
// answers, so any vendorTimeout runs out.
func (w *world) stuck() {
	if w.clock != nil {
		w.clock.pass(time.Hour)
	}
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

func (w *world) send(r request) error {
	w.lastSent = r.headers
	if err := w.start(); err != nil {
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
	if _, err := netip.ParseAddr(host); err != nil && !w.missing[host] && host != "" {
		w.vendor(host)
	}
	conn, err := w.dialProxy()
	if errors.Is(err, errTLSSetup) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return w.exchange(conn, r)
}

// dialProxy opens a connection to the proxy port, with TLS when the app
// uses it. When TLS fails to set up, the error is errTLSSetup and tlsErr
// says why.
func (w *world) dialProxy() (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", w.addr)
	if err != nil || w.appTLS == nil {
		return conn, err
	}
	client := tls.Client(conn, w.appTLS)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	w.tlsErr = client.HandshakeContext(context.Background())
	if w.tlsErr != nil {
		_ = conn.Close()
		return nil, errTLSSetup
	}
	_ = client.SetDeadline(time.Time{})
	w.tlsState = client.ConnectionState()
	return client, nil
}

// exchange sends one request on an open connection and reads the answer.
func (w *world) exchange(conn net.Conn, r request) error {
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
		go w.writeBody(conn, r)
	}
	if r.hangUpAfter > 0 {
		time.Sleep(r.hangUpAfter)
		return nil
	}
	return w.readAnswer(conn)
}

// readAnswer reads the proxy's answer to the request just sent.
func (w *world) readAnswer(conn net.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	w.resp, w.respErr = resp, err
	if err == nil {
		w.respBody, w.respErr = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	return nil
}

func (w *world) writeBody(conn net.Conn, r request) {
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

// settle stops the proxy, which sends every queued record and count.
func (w *world) settle() {
	if w.proxy == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = w.proxy.Shutdown(ctx)
	w.proxy = nil
}

// records are the hotline.request records the first receiver got, once the
// proxy has sent everything.
func (w *world) records() []receivedRecord {
	w.settle()
	records, _ := w.receivers[0].got()
	return records
}

// record is the only record of the scenario.
func (w *world) record() (map[string]any, error) {
	records := w.records()
	if len(records) != 1 {
		return nil, fmt.Errorf("%w: want exactly one record, got %d", errNoRecord, len(records))
	}
	return records[0].attrs, nil
}

// counts waits for the first count before the shutdown, because shutting
// down cancels a send in flight, and with delta totals that send's count is
// gone for good.
func (w *world) counts() []receivedCount {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, counts := w.receivers[0].got(); len(counts) > 0 {
			break
		}
	}
	w.settle()
	_, counts := w.receivers[0].got()
	return counts
}

// syncBuffer collects the proxy's own log lines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func sprint(v any) string { return fmt.Sprint(v) }

// apiFor adds an api for host: the example specs for Talon.One and
// Stripe, a small spec for any other host.
func (w *world) apiFor(host, extra string) string {
	switch host {
	case "api.talon.one":
		return w.talonAPI(extra)
	case "api.stripe.com":
		return w.stripeAPI(extra)
	}
	return w.specAPI(host, "openapi: 3.1.0\npaths:\n  /a:\n    get: {}\n", extra)
}

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}

// waitForRecords waits until the first receiver has n records, while the
// proxy keeps running.
func (w *world) waitForRecords(n int) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if records, _ := w.receivers[0].got(); len(records) >= n {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return failf("%d records did not arrive", n)
}

// waitForVendorCalls waits until the vendor for host got n calls.
func (w *world) waitForVendorCalls(host string, n int) error {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if len(w.vendor(host).received()) >= n {
			return nil
		}
	}
	return failf("the vendor for %s did not get %d calls", host, n)
}
