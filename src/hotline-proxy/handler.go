package proxy

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"hash"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// handler serves the proxy port. It knows four ingresses: the forward
// proxy, where the app puts the full vendor address in the request line;
// the vendor URL in path, where the app puts it after the proxy's own
// address; the Host header, where the app names only the vendor's host;
// and CONNECT, where the app opens a tunnel to the vendor's host and the
// proxy poses as the vendor inside it.
type handler struct {
	// cfg is the config in use. Each call takes it once as it starts, so
	// a reload never changes a call in progress.
	cfg       atomic.Pointer[config.Config]
	clock     Clock
	logger    *slog.Logger
	sink      *export.Sink
	transport *http.Transport
	// internal is the transport for calls that may reach internal IPs.
	internal *http.Transport
	// intercept makes the certificates shown inside tunnels; nil when
	// CONNECT is not an ingress.
	intercept *interceptor
	// tunnels takes each tunnel once it is set up.
	tunnels *tunnels
	// own are the proxy's own numbers.
	own *own
	// buffers are the copy buffers calls borrow.
	buffers bufferPool
	// bodies hold the copies of bodies kept for the checks.
	bodies bodyPool
	// checks run the spec checks of every call.
	checks *checkLine
	// signers sign every integration's records and traces, reusing their
	// buffers between calls.
	signers signPool
	// calls counts calls in flight, including upgraded ones, which the
	// server stops tracking once they take over the connection.
	calls sync.WaitGroup
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.calls.Add(1)
	defer h.calls.Done()
	start := h.clock.Now()
	a := appConnOf(r)
	var ingress string
	var target *url.URL
	var status int
	id := integrationID(r.Header)
	switch {
	case a != nil && a.tunnel != nil:
		// The CONNECT carried the ID for every call in its tunnel.
		ingress, id = "connect", a.tunnel.id
		target, status = a.tunnel.find(r, a)
	case r.Method == http.MethodConnect && h.intercept != nil:
		h.connect(w, r, start)
		return
	default:
		ingress, target, status = findIngress(r)
	}
	if ingress != "" {
		defer h.own.started(ingress)()
	}
	if status != 0 {
		refuse(w, status)
		h.refused(r, start, ingress, id, target, status)
		return
	}
	host := strings.ToLower(target.Hostname())
	cfg := h.cfg.Load()
	api := cfg.API(id, host)
	settings := cfg.NotPassed
	if api != nil {
		settings = api.Settings
	}
	scheme := vendorScheme(r, target, api, settings)
	port := target.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	transport := h.transport
	if settings.AllowInternalIPs {
		transport = h.internal
	}
	c := &call{
		h:          h,
		cfg:        cfg,
		start:      start,
		ingress:    ingress,
		target:     target,
		id:         id,
		api:        api,
		scheme:     scheme,
		host:       host,
		port:       port,
		in:         r,
		timeout:    settings.VendorTimeout,
		transport:  transport,
		reqBody:    &tap{ReadCloser: r.Body, limit: keepRequest(api), mac: macFor(api), pool: &h.bodies, expect: r.ContentLength},
		reqCheck:   newSpecCheck(),
		replyCheck: newSpecCheck(),
	}
	if api != nil && !api.Spec.NamesActionInBody() {
		// A search of a few microseconds, even for a spec as large as AWS
		// SSM's, against a vendor call of milliseconds (Peter, 2026-11-08).
		c.route = api.Spec.Find(spec.Exchange{
			Method: r.Method, EscapedPath: target.EscapedPath(), RawQuery: target.RawQuery, Header: r.Header,
		})
	}
	traced, parent, tracing := h.traced(id, r.Header, start)
	c.trace, c.parent, c.vendorSpan = traced, parent, tracectx.NewSpanID()
	c.appStart, c.appEnd, c.appTimed = a.handshakeTime()
	r.Body = c.reqBody
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	c.cancel = cancel
	defer c.finish()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := *target
			out.Scheme = scheme
			out.Host = net.JoinHostPort(host, port)
			out.Fragment = ""
			pr.Out.URL = &out
			pr.Out.Host = target.Host
			if strings.EqualFold(pr.Out.Header.Get("Upgrade"), "h2c") {
				// A switch to HTTP/2 on the app's own link, which the
				// proxy does not offer: the call stays HTTP/1.1.
				pr.Out.Header.Del("Upgrade")
				pr.Out.Header.Del("Connection")
			}
			if tracing.Forward {
				// The vendor's own spans sit under the proxy's vendor call.
				tracing.Format.Write(pr.Out.Header, traced.WithSpanID(c.vendorSpan))
			}
		},
		Transport:     c,
		BufferPool:    &h.buffers,
		FlushInterval: -1,
		ErrorHandler:  c.fail,
		ErrorLog:      log.New(io.Discard, "", 0),
	}
	app := &toApp{ResponseWriter: w, appLeft: r.Context().Done()}
	c.app = app
	proxy.ServeHTTP(app, r.WithContext(ctx))
	_ = http.NewResponseController(app).Flush()
}

// bufferPool lends each call the 32 KB buffer it copies bodies through, so
// a call does not make a new buffer of its own (Peter, 2026-11-02, found by
// the load test spike). It is the httputil.BufferPool of every call, and
// keeps pointers to slices, so giving a buffer back costs a few bytes, not
// a new buffer.
type bufferPool struct{ pool sync.Pool }

func (p *bufferPool) Get() []byte {
	if b, ok := p.pool.Get().(*[]byte); ok {
		return *b
	}
	return make([]byte, 32<<10)
}

func (p *bufferPool) Put(b []byte) { p.pool.Put(&b) }

// toApp writes the reply to the app and notes whether the app had already
// gone when a part of the reply was ready for it, or a write to it failed.
// Checking before each write, not at the end, keeps an app that leaves
// after reading the whole reply from counting as gone.
type toApp struct {
	http.ResponseWriter
	appLeft <-chan struct{}
	gone    bool
	err     error
}

func (a *toApp) WriteHeader(status int) {
	a.check()
	a.ResponseWriter.WriteHeader(status)
}

func (a *toApp) Write(b []byte) (int, error) {
	a.check()
	n, err := a.ResponseWriter.Write(b)
	a.err = cmp.Or(a.err, err)
	return n, err
}

// FlushError sends what is written so far, and notes it when that fails.
func (a *toApp) FlushError() error {
	err := http.NewResponseController(a.ResponseWriter).Flush()
	a.err = cmp.Or(a.err, err)
	return err
}

// failed says the app did not get the whole reply (Peter, 2026-10-31).
func (a *toApp) failed() bool { return a.gone || a.err != nil }

func (a *toApp) check() {
	select {
	case <-a.appLeft:
		a.gone = true
	default:
	}
}

// Unwrap lets http.ResponseController reach the server's writer to flush.
func (a *toApp) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// findIngress works out which ingress a request used and the vendor address
// it names. A status other than 0 means the proxy refuses the request; the
// address is then whatever part of it the proxy could read, or nil.
func findIngress(r *http.Request) (string, *url.URL, int) {
	switch {
	case r.Method == http.MethodConnect:
		// Not an ingress without listen.connect: the app would encrypt
		// with the vendor itself.
		return "", &url.URL{Host: r.Host}, http.StatusNotImplemented
	case r.URL.IsAbs():
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" || r.URL.Hostname() == "" {
			return "forward_proxy", r.URL, http.StatusBadRequest
		}
		return "forward_proxy", r.URL, 0
	case inPath(r.RequestURI):
		// Only the clean form is used; a squashed slash or an encoded
		// address is refused rather than guessed at.
		lower := strings.ToLower(r.RequestURI)
		if !strings.HasPrefix(lower, "/http://") && !strings.HasPrefix(lower, "/https://") {
			return "url_in_path", nil, http.StatusBadRequest
		}
		target, err := url.Parse(r.RequestURI[1:])
		if err != nil || target.Hostname() == "" {
			return "url_in_path", nil, http.StatusBadRequest
		}
		return "url_in_path", target, 0
	}
	// The Host header ingress: the request names no scheme, so the caller
	// picks one.
	target := &url.URL{Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	if target.Hostname() == "" {
		return "host_header", nil, http.StatusBadRequest
	}
	return "host_header", target, 0
}

// vendorScheme is http or https for the vendor call. TLS origination and
// HTTP/2 from the app always mean https. A Host header call names no
// scheme: it goes out over https, unless its api turned TLS origination off.
func vendorScheme(r *http.Request, target *url.URL, api *config.API, settings config.Settings) string {
	switch {
	case settings.TLSOrigination, r.ProtoMajor == 2:
		return "https"
	case target.Scheme != "":
		return target.Scheme
	case api != nil:
		return "http"
	}
	return "https"
}

// inPath says whether a raw request target starts with a vendor address,
// clean or damaged: /http: or /https:, the colon plain or encoded.
func inPath(requestURI string) bool {
	lower := strings.ToLower(requestURI)
	for _, prefix := range []string{"/http:", "/https:", "/http%3a", "/https%3a"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// refuse answers a call the proxy does not forward.
func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Proxy-Status", "hotline; error=http_request_error")
	w.WriteHeader(status)
}

// traced is a call's trace context: the app's trace id and flags, or a new
// trace id, sampled, when the app sent none the proxy can read, and a new
// span id for the proxy's call span. It comes with the app's span id, the
// call span's parent, not valid when there is none, and with the
// integration's trace settings.
func (h *handler) traced(id string, header http.Header, start time.Time) (trace.SpanContext, trace.SpanID, config.Trace) {
	settings := h.cfg.Load().Trace(id)
	app, ok := settings.Format.Read(header)
	traceID, flags, parent := app.TraceID(), app.TraceFlags(), app.SpanID()
	if !ok {
		traceID, flags, parent = settings.Format.NewTraceID(start), trace.FlagsSampled, trace.SpanID{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     tracectx.NewSpanID(),
		TraceFlags: flags,
	}), parent, settings
}

// recordContext carries a record's trace context to the exporters, which
// put it in the record's trace_id, span_id and flags.
func recordContext(sc trace.SpanContext) context.Context {
	return trace.ContextWithSpanContext(context.Background(), sc)
}

// integrationID reads the user name from "Proxy-Authorization: Basic".
// Anything else, including a broken or repeated header, counts as no ID.
func integrationID(h http.Header) string {
	values := h.Values("Proxy-Authorization")
	if len(values) != 1 {
		return ""
	}
	scheme, credentials, _ := strings.Cut(values[0], " ")
	if !strings.EqualFold(scheme, "basic") {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(credentials)
	if err != nil {
		return ""
	}
	id, _, found := strings.Cut(string(decoded), ":")
	if !found {
		return ""
	}
	return id
}

// call is one call through the proxy, from the app's request to the record.
type call struct {
	h *handler
	// cfg is the config the call started with.
	cfg     *config.Config
	start   time.Time
	ingress string
	// trace is the call's trace context, which its record carries; its
	// span id is the call span's. parent is the app's span, the call
	// span's parent, and vendorSpan the id of the vendor call's span.
	trace      trace.SpanContext
	parent     trace.SpanID
	vendorSpan trace.SpanID
	// target is the vendor address the app named.
	target  *url.URL
	id      string
	api     *config.API
	scheme  string
	host    string
	port    string
	in      *http.Request
	timeout time.Duration
	// appStart and appEnd are when setting up TLS with the app started
	// and ended, when this is the first call on its connection and
	// appTimed is set.
	appStart time.Time
	appEnd   time.Time
	appTimed bool
	// transport makes the vendor call.
	transport *http.Transport
	cancel    context.CancelFunc
	app       *toApp

	reqBody  *tap
	respBody *tap
	timedOut atomic.Bool
	// reqCheck and replyCheck check each side against the spec as soon as
	// it is whole.
	reqCheck   *specCheck
	replyCheck *specCheck
	// route is the call's route in the spec, found once. For most specs
	// the call finds it as it starts, before any check exists. For a spec
	// that names its action in the body, the request's check finds it, and
	// the reply's check and the record read it only once that check ended.
	route spec.Route

	mu        sync.Mutex
	phase     phase
	times     map[phase][2]time.Time
	firstByte time.Time
	vendorIn  time.Time
	vendorOut time.Time
	// gotAt is when the call had its connection, and wrote when the
	// call was fully sent on it.
	gotAt     time.Time
	wrote     time.Time
	connected bool
	reused    bool
	peer      string
	looked    string
	resp      *http.Response
	status    int
	errorType string
	// failedStep is the step the call failed in, if one did.
	failedStep string
}

type phase string

const (
	phaseDNS     phase = "dns"
	phaseConnect phase = "connect"
	phaseTLS     phase = "tls"
	phaseReply   phase = "reply"
)

// RoundTrip makes the vendor call. It waits for the vendor's answer up to
// vendorTimeout; the app giving up does not stop it.
func (c *call) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mark(func() { c.vendorIn = c.h.clock.Now() })
	c.times = map[phase][2]time.Time{}
	trace := &httptrace.ClientTrace{
		GetConn:              func(string) { c.mark(func() { c.phase = phaseReply }) },
		DNSStart:             func(i httptrace.DNSStartInfo) { c.lookup(i.Host) },
		DNSDone:              func(httptrace.DNSDoneInfo) { c.end(phaseDNS) },
		ConnectStart:         func(string, string) { c.begin(phaseConnect) },
		ConnectDone:          func(string, string, error) { c.end(phaseConnect) },
		TLSHandshakeStart:    func() { c.begin(phaseTLS) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { c.end(phaseTLS) },
		GotConn:              c.gotConn,
		WroteRequest:         c.wroteRequest,
		GotFirstResponseByte: func() { c.mark(func() { c.firstByte = c.h.clock.Now() }) },
	}
	ctx := context.WithValue(req.Context(), deadlineKey{}, c.h.clock.Now().Add(c.timeout))
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))
	timer := c.h.clock.AfterFunc(c.timeout, func() {
		c.timedOut.Store(true)
		c.cancel()
	})
	resp, err := c.transport.RoundTrip(req)
	timer.Stop()
	if err != nil {
		return nil, err
	}
	c.respBody = &tap{ReadCloser: resp.Body, limit: keepReply(c.api), mac: macFor(c.api), pool: &c.h.bodies, expect: resp.ContentLength, done: func() {
		out := c.h.clock.Now()
		c.mark(func() { c.vendorOut = out })
		c.checkReply(out, resp)
	}}
	if both, ok := resp.Body.(io.Writer); ok && resp.StatusCode == http.StatusSwitchingProtocols {
		// An upgraded connection, such as a WebSocket, carries data both
		// ways, so the proxy must be able to write to it too. It may stay
		// quiet for as long as both sides like.
		resp.Body = upgraded{tap: c.respBody, Writer: both}
	} else {
		c.respBody.wait = c.waitForVendor
		resp.Body = c.respBody
	}
	c.mark(func() { c.resp = resp; c.status = resp.StatusCode })
	return resp, nil
}

// waitForVendor limits one wait for the next part of the reply to
// vendorTimeout, so a vendor that goes quiet can't hold the call forever.
// It returns the function that ends the wait.
func (c *call) waitForVendor() func() bool {
	return c.h.clock.AfterFunc(c.timeout, c.cancel).Stop
}

func (c *call) mark(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

// begin starts a phase. A phase tried again, such as connecting to the next
// address of a host, keeps its first start, so no time is counted twice.
// Once the call has its connection, events from a connection still being
// opened in the background belong to no call and are ignored.
func (c *call) begin(p phase) {
	c.mark(func() {
		if c.connected {
			return
		}
		c.phase = p
		if c.times[p][0].IsZero() {
			c.times[p] = [2]time.Time{c.h.clock.Now()}
		}
	})
}

func (c *call) lookup(host string) {
	c.begin(phaseDNS)
	c.mark(func() {
		if !c.connected {
			c.looked = host
		}
	})
}

func (c *call) end(p phase) {
	c.mark(func() {
		if c.connected {
			return
		}
		t := c.times[p]
		t[1] = c.h.clock.Now()
		c.times[p] = t
	})
}

func (c *call) gotConn(info httptrace.GotConnInfo) {
	c.mark(func() {
		c.phase = phaseReply
		c.connected = true
		c.gotAt = c.h.clock.Now()
		c.reused = info.Reused
		if info.Reused {
			// A lookup or connect already started was for a connection
			// this call doesn't use.
			c.times = map[phase][2]time.Time{}
			c.looked = ""
		}
		c.peer, _, _ = net.SplitHostPort(info.Conn.RemoteAddr().String())
	})
}

// wroteRequest notes when the call was fully sent. A call that broke off
// while it was sent was never fully sent.
func (c *call) wroteRequest(info httptrace.WroteRequestInfo) {
	if info.Err == nil {
		wrote := c.h.clock.Now()
		c.mark(func() { c.wrote = wrote })
		c.checkRequest(wrote)
	}
}

// fail answers the app when the proxy could not get a reply from the
// vendor, with the RFC 9209 error name in Proxy-Status.
func (c *call) fail(w http.ResponseWriter, _ *http.Request, err error) {
	c.mu.Lock()
	status, name := classify(c.phase, c.timedOut.Load(), err)
	c.status, c.errorType = status, name
	c.vendorOut = c.h.clock.Now()
	// A call refused before the vendor call started, such as one asking
	// to switch to a protocol with a broken name, starts it as it fails.
	c.vendorIn = orTime(c.vendorIn, c.vendorOut)
	if !errors.Is(err, errProhibited) {
		c.failedStep = c.step()
	}
	c.mu.Unlock()
	w.Header().Set("Proxy-Status", "hotline; error="+name)
	w.WriteHeader(status)
}

// step is the name of the step the call is in.
func (c *call) step() string {
	switch {
	case c.phase == phaseDNS:
		return "dns lookup"
	case c.phase == phaseConnect:
		return "tcp connect"
	case c.phase == phaseTLS:
		return "vendor tls handshake"
	case c.wrote.IsZero():
		return "send call"
	}
	// No reply came, or the proxy would not be here.
	return "wait for vendor"
}

func classify(p phase, timedOut bool, err error) (int, string) {
	switch {
	case errors.Is(err, errProhibited):
		return http.StatusForbidden, "destination_ip_prohibited"
	case p == phaseDNS && timedOut:
		return http.StatusGatewayTimeout, "dns_timeout"
	case p == phaseDNS:
		return http.StatusBadGateway, "dns_error"
	case (p == phaseConnect || p == phaseTLS) && timedOut:
		return http.StatusGatewayTimeout, "connection_timeout"
	case p == phaseConnect && errors.Is(err, syscall.ECONNREFUSED):
		return http.StatusBadGateway, "connection_refused"
	case p == phaseConnect:
		return http.StatusBadGateway, "destination_unavailable"
	case p == phaseTLS && isCertificateError(err):
		return http.StatusBadGateway, "tls_certificate_error"
	case p == phaseTLS:
		return http.StatusBadGateway, "tls_protocol_error"
	case timedOut:
		return http.StatusGatewayTimeout, "http_response_timeout"
	}
	return http.StatusBadGateway, "http_response_incomplete"
}

// upgraded is the vendor's side of an upgraded connection: read through
// the tap, written straight to the vendor.
type upgraded struct {
	*tap
	io.Writer
}

// keptBody is how much of each body the proxy keeps for the checks.
const keptBody = 2 << 20

// keepRequest is how much of the request body to keep: none for a call
// that is not checked, and none when the api checks no body, unless the
// spec names the route's action in the body.
func keepRequest(api *config.API) int {
	if api == nil || !api.Checks.Has(config.PartBody) && !api.Spec.NamesActionInBody() {
		return 0
	}
	return keptBody
}

// keepReply is how much of the reply body to keep: none when it is not
// checked.
func keepReply(api *config.API) int {
	if api == nil || !api.Checks.Has(config.PartReplyBody) {
		return 0
	}
	return keptBody
}

// macFor signs a body with the integration's key as it passes: none for a
// call without a full record.
func macFor(api *config.API) hash.Hash {
	if api == nil {
		return nil
	}
	return hmac.New(sha256.New, api.SigningKey)
}

// tap passes a body straight on and counts the bytes that passed. It keeps
// a copy of up to limit bytes for the checks, and signs every byte with
// mac, if set (Peter, 2026-10-31). done, if
// set, runs once when the body ends: read to the end, broken off, or closed
// early. The request body is read by the transport, which may still be
// reading when the record is made, so the counts are behind a lock.
type tap struct {
	io.ReadCloser
	done func()
	// wait, if set, runs before each read and limits how long it may take.
	wait func() func() bool

	limit int
	mac   hash.Hash
	// pool lends the buffer the copy is kept in; expect is the body's
	// Content-Length, or -1 when it was not sent.
	pool   *bodyPool
	expect int64

	mu   sync.Mutex
	size int64
	err  error
	kept []byte
	// buf is the borrowed buffer kept sits in, nil until the first byte.
	buf  *bodyBuffer
	over bool
	// handed is set once the copy has gone to the check: from then on the
	// copy never changes, and its buffer is never swapped.
	handed bool
}

func (t *tap) Read(p []byte) (int, error) {
	if t.wait != nil {
		defer t.wait()()
	}
	n, err := t.ReadCloser.Read(p)
	t.mu.Lock()
	t.size += int64(n)
	if t.mac != nil {
		t.mac.Write(p[:n])
	}
	if t.limit > 0 && !t.handed {
		room := min(n, t.limit-len(t.kept))
		t.keep(p[:room])
		t.over = t.over || room < n
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	t.mu.Unlock()
	if err != nil {
		t.finish()
	}
	return n, err
}

func (t *tap) Close() error {
	t.finish()
	return t.ReadCloser.Close()
}

// counts says how many bytes passed so far, and why the body broke off,
// if it did.
func (t *tap) counts() (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size, t.err
}

// signed is the signature of the bytes that passed so far, empty when none
// did.
func (t *tap) signed() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.size == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(t.mac.Sum(nil))
}

// keep adds b to the copy. The first bytes borrow a buffer the size of the
// whole body, when its Content-Length says so; a copy that outgrows its
// buffer moves to a larger one and gives the old one back.
func (t *tap) keep(b []byte) {
	if len(b) == 0 {
		return
	}
	need := len(t.kept) + len(b)
	if t.buf == nil || need > cap(t.buf.data) {
		want := int64(need)
		if t.buf == nil && t.expect > want {
			want = t.expect
		}
		buf := t.pool.get(int(min(want, int64(t.limit))))
		buf.data = append(buf.data, t.kept...)
		if t.buf != nil {
			t.pool.put(t.buf)
		}
		t.buf = buf
	}
	t.buf.data = append(t.buf.data, b...)
	t.kept = t.buf.data
}

// held is the memory the copy's buffer takes, or none before the first
// byte.
func (t *tap) held() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.buf == nil {
		return 0
	}
	return int64(cap(t.buf.data))
}

// drop gives the copy's buffer back, once the check of the copy has ended.
func (t *tap) drop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.buf != nil {
		t.pool.put(t.buf)
	}
	t.kept, t.buf, t.limit = nil, nil, 0
}

// copied is the copy of the body kept so far. The check reads it after the
// lock is let go, so the copy stays as it is until drop.
func (t *tap) copied() spec.Body {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handed = true
	return spec.Body{Data: t.kept, TooLarge: t.over}
}

// bodyPool lends the buffers bodies are kept in, in eight sizes from 16 KB
// to 2 MiB, each twice the one before, so a buffer given back fits the
// next body of about its size (Peter, 2026-11-02, found by the load test).
type bodyPool struct{ sizes [8]sync.Pool }

// bodyBuffer is a borrowed buffer and the pool of its size.
type bodyBuffer struct {
	data []byte
	size int
}

// get lends an empty buffer of at least n bytes, up to the largest size.
func (p *bodyPool) get(n int) *bodyBuffer {
	i := 0
	for 16<<10<<i < n && i < len(p.sizes)-1 {
		i++
	}
	if b, ok := p.sizes[i].Get().(*bodyBuffer); ok {
		b.data = b.data[:0]
		return b
	}
	return &bodyBuffer{data: make([]byte, 0, 16<<10<<i), size: i}
}

// put gives a buffer back to the pool of its size.
func (p *bodyPool) put(b *bodyBuffer) { p.sizes[b.size].Put(b) }

func (t *tap) finish() {
	if t.done != nil {
		t.done()
		t.done = nil
	}
}
