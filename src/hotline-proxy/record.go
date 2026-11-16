package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// finish ends the call once the reply has gone back to the app. A side not
// yet checked, such as a request never sent, is checked now, when the call
// has a spec. The record and trace are made once the checks end, in the
// background, as for every call.
func (c *call) finish() {
	end := c.h.clock.Now()
	if c.api != nil {
		c.checkRequest(end)
		c.mu.Lock()
		resp := c.resp
		c.mu.Unlock()
		c.checkReply(end, resp)
	}
	c.h.recordLater(func() finished { return c.record(end) })
}

// recordRoom is the room made for a full record's attributes: the call's own,
// what the spec check found, and the ledger's hashes and place.
const recordRoom = 44

// failure is what went wrong with a call, if anything: the RFC 9209 error
// name, and the step it went wrong in.
type failure struct {
	errorType string
	step      string
}

// failure is how the call failed: as fail noted, or, for a reply the
// vendor broke off after it began, in the step that received it. It only
// reads the call, which is locked.
func (c *call) failure() failure {
	if c.respBody != nil {
		if _, err := c.respBody.counts(); err != nil {
			return failure{errorType: "http_response_incomplete", step: stepReceive}
		}
	}
	return failure{errorType: c.errorType, step: c.failedStep}
}

// record builds the call's record and trace, once its checks have ended.
func (c *call) record(end time.Time) finished {
	c.reqCheck.wait()
	replied := c.replyCheck.wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	// A full record holds about 30 attributes; the room is made once, so the
	// list is not copied again as it grows.
	attrs := make([]attribute.KeyValue, 0, recordRoom)
	attrs = append(attrs,
		attribute.String("hotline.integration.id", orNotPassed(c.id)),
		attribute.String("hotline.ingress", c.ingress),
		clientAddress(c.in),
		semconv.ServerAddress(c.host),
		semconv.HTTPRequestMethodKey.String(c.in.Method),
		semconv.HTTPResponseStatusCode(c.status),
	)
	attrs = append(attrs, c.process.attributes()...)
	failed := c.failure()
	if failed.errorType != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String(failed.errorType))
	}
	if c.peer != "" {
		// The IP the proxy connected to, after DNS and any try at a
		// second address. Every record has it, minimal ones too.
		attrs = append(attrs, semconv.NetworkPeerAddress(c.peer))
	}
	opened := c.opened()
	attrs = append(attrs, attribute.Int64("hotline.timing.total_us", micros(opened, end)))
	spans := c.spans(opened, end, failed)
	if c.api != nil {
		req, reply := c.reqCheck.found, c.replyCheck.found
		requested := c.reqCheck.started.Load()
		attrs = append(attrs, c.full(req.With(reply), requested || replied)...)
		// Each side was checked alongside the call, so its step may end
		// after the call span (Peter, 2026-10-31).
		if requested {
			spans = append(spans, c.checkSpans("request spec check", c.reqCheck, req.Request, req.Skipped)...)
		}
		if replied {
			spans = append(spans, c.checkSpans("reply spec check", c.replyCheck, reply.Reply, reply.Skipped)...)
		}
	} else {
		// A minimal record carries the sizes too, counted as the bodies
		// passed and never kept (Peter, 2026-11-08). The reply's size is there
		// only when a reply came.
		sent, _ := c.reqBody.counts()
		attrs = append(attrs, semconv.HTTPRequestBodySize(int(sent)))
		if c.respBody != nil {
			got, _ := c.respBody.counts()
			attrs = append(attrs, semconv.HTTPResponseBodySize(int(got)))
		}
		// The address as sent, when the integration asks for it; with no
		// spec there is nothing to say what to redact (Peter, 2026-11-08).
		if c.integration.URLFull {
			attrs = append(attrs, semconv.URLFull(c.sentAddress()))
		}
	}
	// Every body that passed has its hash, minimal record or full, an
	// empty one too, so each is in the ledger (Peter, 2026-11-16).
	attrs = append(attrs, attribute.String("hotline.request.body.hash", c.reqBody.hashed()))
	reply := emptyBodyHash("response", c.trace)
	if c.respBody != nil {
		reply = c.respBody.hashed()
	}
	attrs = append(attrs, attribute.String("hotline.response.body.hash", reply))
	var content []attribute.KeyValue
	if c.integration.Content.Full {
		content = c.content()
		attrs = append(attrs, attribute.String("hotline.content.hash", ledger.Text(ledger.ContentHash(content))))
		content = c.withBodies(content)
	}
	return finished{cfg: c.cfg, chain: c.integration.Name, sc: c.trace, at: c.start, attrs: attrs, spans: spans, content: content}
}

// content is what content: full adds to the record for the file
// exporters: url.full as sent and every header of the call and its reply,
// as they were, under hotline.content.* names, beside the record, which
// stays as its hash covers it (Peter, 2026-11-16). Proxy-Authorization
// holds the integration's login, so its value is REDACTED; other proxy
// headers are kept as sent. A body cut at maxBodySize is flagged.
func (c *call) content() []attribute.KeyValue {
	var reply http.Header
	if c.resp != nil {
		reply = c.resp.Header
	}
	full := make([]attribute.KeyValue, 0, len(c.in.Header)+len(reply)+5)
	full = append(full, attribute.String("hotline.content.url.full", c.sentAddress()))
	for name, values := range c.in.Header {
		if strings.EqualFold(name, "Proxy-Authorization") {
			values = []string{"REDACTED"}
		}
		full = append(full, attribute.StringSlice("hotline.content.request.header."+strings.ToLower(name), values))
	}
	for name, values := range reply {
		full = append(full, attribute.StringSlice("hotline.content.response.header."+strings.ToLower(name), values))
	}
	if _, cut := c.reqBody.savedBody(); cut {
		full = append(full, attribute.Bool("hotline.content.request.body.truncated", true))
	}
	if c.respBody != nil {
		if _, cut := c.respBody.savedBody(); cut {
			full = append(full, attribute.Bool("hotline.content.response.body.truncated", true))
		}
	}
	return full
}

// withBodies adds the kept part of each body, as bytes, which the file
// writes as base64. Each has its own hash in the record already.
func (c *call) withBodies(full []attribute.KeyValue) []attribute.KeyValue {
	if body, _ := c.reqBody.savedBody(); len(body) > 0 {
		full = append(full, attribute.ByteSlice("hotline.content.request.body", body))
	}
	if c.respBody != nil {
		if body, _ := c.respBody.savedBody(); len(body) > 0 {
			full = append(full, attribute.ByteSlice("hotline.content.response.body", body))
		}
	}
	return full
}

// sentAddress is the address the call went to, as the app sent it: the scheme
// the proxy used, no user and password, the path and query as written.
func (c *call) sentAddress() string {
	out := *c.target
	out.Scheme, out.User, out.Fragment = c.scheme, nil, ""
	out.Host = c.host
	if c.target.Port() != "" {
		out.Host = net.JoinHostPort(c.host, c.port)
	}
	return out.String()
}

// checkSpans are the step of one side's check, error when that side broke
// the spec, ok when it passed, unset when part of it was skipped, and under
// it the wait for a free worker. The wait is an error with error.type
// overloaded when the check gave way in a full line (Peter, 2026-11-02).
func (c *call) checkSpans(name string, s *specCheck, found []spec.Result, skipped string) []export.Span {
	check := export.Span{
		ID: tracectx.NewSpanID(), Parent: c.trace.SpanID(), Name: name, Kind: trace.SpanKindInternal,
		Start: s.start, End: s.end, Status: export.FailedIf(len(found) > 0),
	}
	if len(found) == 0 && skipped == "" {
		check.Status = export.StatusOK
	}
	wait := export.Span{
		ID: tracectx.NewSpanID(), Parent: check.ID, Name: "wait for check worker", Kind: trace.SpanKindInternal,
		Start: s.start, End: s.waited, Status: export.FailedIf(s.dropped),
	}
	if s.dropped {
		wait.Attrs = []attribute.KeyValue{semconv.ErrorTypeKey.String("overloaded")}
	}
	return []export.Span{check, wait}
}

// refused records a request the proxy read but did not forward, with a
// minimal record and a trace of the call span alone. The vendor host is in
// it only when the request named one.
func (h *handler) refused(r *http.Request, start time.Time, cfg *config.Config, ingress, id string, target *url.URL, status int) {
	s := short{
		id: id, ingress: ingress, client: clientIP(r.RemoteAddr), method: r.Method, status: status,
		errorType: "http_request_error", header: r.Header, start: start, cfg: cfg, exchange: true,
	}
	if target != nil {
		s.host = strings.ToLower(target.Hostname())
	}
	h.recordShort(s)
}

// short is a record of something that never became a forwarded call: a
// request the proxy refused, a tunnel whose TLS failed, or a connection
// an intercepting ingress relayed untouched or dropped. Each place fills
// it in and recordShort turns it into the record and its trace, so these
// records keep one shape (Peter, 2026-11-16, from the decomplect review).
type short struct {
	id string
	// ingress, host, method and errorType are left out of the record when
	// empty, and status when 0.
	ingress   string
	client    string
	host      string
	method    string
	status    int
	errorType string
	process   process
	// extra are the attributes only this kind of record has, such as
	// hotline.intercept.mode.
	extra []attribute.KeyValue
	// header is the request's headers, read for the app's trace context;
	// nil when there was no request.
	header http.Header
	start  time.Time
	// cfg is the config in use when the connection or request arrived. A
	// relay can last long past a reload, and its record still belongs to
	// the config it ran under (ledger decision 11).
	cfg *config.Config
	// exchange says the app sent an HTTP request. Only then does the
	// record carry each side's body hash, of zero bytes, since no body was
	// read (ledger decision 8, Peter, 2026-11-16).
	exchange bool
	// appTLS, when set, is when the failed TLS setup with the app started
	// and ended, a step under the call span.
	appTLS *[2]time.Time
}

// recordShort records s, minimal, with a trace of the call span and any
// step under it. The call span failed when something went wrong.
func (h *handler) recordShort(s short) {
	end := h.clock.Now()
	attrs := []attribute.KeyValue{attribute.String("hotline.integration.id", orNotPassed(s.id))}
	if s.ingress != "" {
		attrs = append(attrs, attribute.String("hotline.ingress", s.ingress))
	}
	attrs = append(attrs, semconv.ClientAddress(s.client))
	attrs = append(attrs, s.process.attributes()...)
	if s.host != "" {
		attrs = append(attrs, semconv.ServerAddress(s.host))
	}
	if s.method != "" {
		attrs = append(attrs, semconv.HTTPRequestMethodKey.String(s.method))
	}
	if s.status != 0 {
		attrs = append(attrs, semconv.HTTPResponseStatusCode(s.status))
	}
	attrs = append(attrs, s.extra...)
	if s.errorType != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String(s.errorType))
	}
	attrs = append(attrs, attribute.Int64("hotline.timing.total_us", micros(s.start, end)))
	integration := s.cfg.Integration(s.id)
	traced, parent, _ := traceOf(integration, s.header, s.start)
	if s.exchange {
		attrs = append(attrs,
			attribute.String("hotline.request.body.hash", emptyBodyHash("request", traced)),
			attribute.String("hotline.response.body.hash", emptyBodyHash("response", traced)))
	}
	spans := []export.Span{callSpan(traced, parent, s.start, end, s.errorType != "")}
	if s.appTLS != nil {
		spans = append(spans, export.Span{ID: tracectx.NewSpanID(), Parent: traced.SpanID(), Name: stepAppTLS,
			Kind: trace.SpanKindInternal, Start: s.appTLS[0], End: s.appTLS[1], Status: export.StatusError})
	}
	h.recordLater(recorded(finished{cfg: s.cfg, chain: integration.Name, sc: traced, at: s.start, attrs: attrs, spans: spans}))
}

// finished is what a call leaves: its record and its trace.
type finished struct {
	// cfg is the config the call started with. The ledger puts the record
	// in a batch of that config, even when a reload came before the
	// record was made (Peter, 2026-11-16).
	cfg *config.Config
	// chain is the ledger chain the record goes in: its integration's
	// name in that config, or not-passed.
	chain string
	sc    trace.SpanContext
	at    time.Time
	attrs []attribute.KeyValue
	spans []export.Span
	// content, under content: full, is what file exporters get besides the
	// record.
	content []attribute.KeyValue
}

// recordLater is the one way every call is recorded: in the background,
// apart from the call, so the app's connection is free as soon as its reply
// is sent (Peter, 2026-11-12). build makes the record and trace, which are
// then handed to the exporters and measured for the proxy's own numbers.
// Shutdown waits for it, as for a call in flight. It is only called while
// the call itself is still counted in flight.
func (h *handler) recordLater(build func() finished) {
	h.calls.Add(1)
	go func() {
		defer h.calls.Done()
		f := build()
		h.book.record(recordContext(f.sc), f)
		h.own.ended(f.attrs)
	}()
}

// recorded is a record already made, for recordLater.
func recorded(f finished) func() finished { return func() finished { return f } }

// clientAddress is the IP address the app's connection came from, never a
// header the app could set (Peter, 2026-10-31).
func clientAddress(r *http.Request) attribute.KeyValue {
	return semconv.ClientAddress(clientIP(r.RemoteAddr))
}

// clientIP is the IP of an address with a port.
func clientIP(address string) string {
	host, _, _ := net.SplitHostPort(address)
	return host
}

// micros is the time from one moment to the next in whole microseconds,
// from their wall clock readings, which the trace's spans carry. So a
// record's hotline.timing.total_us is exactly its call span's length.
// end.Sub(start) would use the monotonic readings instead, which differ
// from the wall clock's by a few nanoseconds, and so by a whole
// microsecond in about one call in 150.
func micros(from, to time.Time) int64 {
	return (to.UnixNano() - from.UnixNano()) / 1000
}

func orNotPassed(id string) string {
	if id == "" {
		return "not-passed"
	}
	return id
}

// full is the rest of the record, for calls with a known integration and
// api, and the status of the check against the spec, which is part of it.
func (c *call) full(found spec.Checked, ran bool) []attribute.KeyValue {
	origin := c.scheme + "://" + c.host
	if c.target.Port() != "" {
		origin += ":" + c.port
	}
	var replyHeaders map[string][]string
	if c.resp != nil {
		replyHeaders = c.resp.Header
	}
	// record waited for both checks, so the route is found by now.
	redacted := c.api.Spec.Redact(c.specRoute(), spec.Call{
		Origin:       origin,
		EscapedPath:  c.target.EscapedPath(),
		RawQuery:     c.target.RawQuery,
		Header:       c.in.Header,
		ReplyHeaders: replyHeaders,
	})
	port, _ := strconv.Atoi(c.port)
	sent, _ := c.reqBody.counts()
	attrs := make([]attribute.KeyValue, 0, 16+len(redacted.Header)+len(redacted.ReplyHeaders))
	attrs = append(attrs,
		semconv.URLScheme(c.scheme),
		semconv.ServerPort(port),
		semconv.HTTPRequestBodySize(int(sent)),
		attribute.Bool("hotline.client.cancelled", c.app.failed()),
		attribute.String("hotline.config.version", c.cfg.Label()),
	)
	if redacted.Template != "" {
		attrs = append(attrs, semconv.URLFull(redacted.URLFull), semconv.URLTemplate(redacted.Template))
	}
	if c.resp != nil {
		got, _ := c.respBody.counts()
		attrs = append(attrs,
			semconv.NetworkProtocolVersion(strings.TrimSuffix(strings.TrimPrefix(c.resp.Proto, "HTTP/"), ".0")),
			semconv.HTTPResponseBodySize(int(got)))
	}
	if c.resp != nil && c.resp.TLS != nil {
		attrs = append(attrs,
			semconv.TLSProtocolVersion(strings.TrimPrefix(tls.VersionName(c.resp.TLS.Version), "TLS ")),
			semconv.TLSServerNotAfter(c.resp.TLS.PeerCertificates[0].NotAfter.UTC().Format(time.RFC3339)))
	}
	for name, values := range redacted.Header {
		attrs = append(attrs, attribute.StringSlice("http.request.header."+name, values))
	}
	for name, values := range redacted.ReplyHeaders {
		attrs = append(attrs, attribute.StringSlice("http.response.header."+name, values))
	}
	return append(attrs, checked(found, ran)...)
}

// shownResults is how many results of one side a record holds at most.
const shownResults = 20

// checked is what the checks found, as the record holds it, with the
// status: valid, invalid, or skipped when nothing was found but part of
// the check did not run (Peter, 2026-10-31).
func checked(found spec.Checked, ran bool) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	for _, f := range found.Copied {
		switch v := f.Value.(type) {
		case string:
			attrs = append(attrs, attribute.String(f.Name, v))
		case bool:
			attrs = append(attrs, attribute.Bool(f.Name, v))
		case int64:
			attrs = append(attrs, attribute.Int64(f.Name, v))
		case float64:
			attrs = append(attrs, attribute.Float64(f.Name, v))
		}
	}
	status := "valid"
	if found.Skipped != "" {
		attrs = append(attrs, attribute.String("hotline.schema.skipped", found.Skipped))
		status = "skipped"
	}
	if len(found.Request) > 0 || len(found.Reply) > 0 {
		status = "invalid"
	}

	// A fixed pair, not a map built for every call.
	sides := [2]struct {
		name    string
		results []spec.Result
	}{{"request", found.Request}, {"response", found.Reply}}
	for _, one := range sides {
		side, results := one.name, one.results
		if len(results) == 0 {
			continue
		}
		key := "hotline.schema." + side + ".errors"
		if len(results) > shownResults {
			attrs = append(attrs, attribute.Int(key+"_total", len(results)))
			results = results[:shownResults]
		}
		values := make([]attribute.Value, len(results))
		for i, r := range results {
			entry := []attribute.KeyValue{attribute.String("part", r.Part), attribute.String("rule", r.Rule)}
			if r.Place != "" {
				entry = append(entry, attribute.String("place", r.Place))
			}
			if r.Expected != "" {
				entry = append(entry, attribute.String("expected", r.Expected))
			}
			values[i] = attribute.MapValue(entry...)
		}
		attrs = append(attrs, attribute.Slice(key, values...))
	}
	if !ran {
		// No part was checked, so the call is neither valid nor invalid.
		return attrs
	}
	return append(attrs, attribute.String("hotline.schema.status", status))
}
