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

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// finish ends the call once the reply has gone back to the app. A side not
// yet checked, such as a request never sent, is checked now. The record and
// trace are made once the checks end, apart from the call, so the app never
// waits for them; Shutdown does.
func (c *call) finish() {
	end := c.h.clock.Now()
	if c.api == nil {
		c.record(end)
		return
	}
	c.checkRequest(end)
	c.mu.Lock()
	resp := c.resp
	c.mu.Unlock()
	c.checkReply(end, resp)
	c.h.calls.Add(1)
	go func() {
		defer c.h.calls.Done()
		c.record(end)
	}()
}

// record builds the call's record and trace, and hands them to the
// exporters without waiting for them.
// recordRoom is the room made for a full record's attributes: the call's own,
// what the spec check found, and the two signatures.
const recordRoom = 40

func (c *call) record(end time.Time) {
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
	if c.respBody != nil {
		if _, err := c.respBody.counts(); err != nil {
			c.errorType, c.failedStep = "http_response_incomplete", "receive reply"
		}
	}
	if c.errorType != "" {
		attrs = append(attrs, semconv.ErrorTypeKey.String(c.errorType))
	}
	if c.peer != "" {
		// The IP the proxy connected to, after DNS and any try at a
		// second address. Every record has it, minimal ones too.
		attrs = append(attrs, semconv.NetworkPeerAddress(c.peer))
	}
	opened := c.opened()
	attrs = append(attrs, attribute.Int64("hotline.timing.total_us", end.Sub(opened).Microseconds()))
	spans := c.spans(opened, end)
	content := c.cfg.Content(c.id)
	var full []attribute.KeyValue
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
		key := c.api.SigningKey
		traced := c.h.signers.traceSignature(key, c.trace, spans)
		if content.Full {
			// The file's record is signed on its own, with every header
			// in it; the bodies are signed already, whole, as they passed.
			full = append(c.withContent(attrs), traced)
			full = append(full, c.h.signers.signature(key, full, c.start, c.trace)...)
			full = c.withBodies(full)
		}
		attrs = append(attrs, traced)
		attrs = append(attrs, c.h.signers.signature(key, attrs, c.start, c.trace)...)
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
		if c.cfg.URLFull(c.id) {
			attrs = append(attrs, semconv.URLFull(c.sentAddress()))
		}
		if content.Full {
			// Unsigned, as minimal records are: there is no spec to sign
			// against (Peter, 2026-11-11).
			full = c.withBodies(c.withContent(attrs))
		}
	}
	c.h.record(c.in, finished{
		sc: c.trace, at: c.start, attrs: attrs, spans: spans, full: full, address: c.sentAddress(),
	})
}

// withContent is a new record with every attribute of attrs but the
// headers and url.full, and then url.full as sent and every header of the
// call and its reply, as they were, for content: full (Peter, 2026-11-11).
// Proxy-Authorization holds the integration's login, so its value is
// REDACTED; other proxy headers are kept as sent. A body cut at
// maxBodySize is flagged.
func (c *call) withContent(attrs []attribute.KeyValue) []attribute.KeyValue {
	var reply http.Header
	if c.resp != nil {
		reply = c.resp.Header
	}
	full := make([]attribute.KeyValue, 0, len(attrs)+len(c.in.Header)+len(reply)+8)
	for _, kv := range attrs {
		key := string(kv.Key)
		if kv.Key == semconv.URLFullKey || strings.HasPrefix(key, "http.request.header.") || strings.HasPrefix(key, "http.response.header.") {
			continue
		}
		full = append(full, kv)
	}
	full = append(full, semconv.URLFull(c.sentAddress()))
	for name, values := range c.in.Header {
		if strings.EqualFold(name, "Proxy-Authorization") {
			values = []string{"REDACTED"}
		}
		full = append(full, attribute.StringSlice("http.request.header."+strings.ToLower(name), values))
	}
	for name, values := range reply {
		full = append(full, attribute.StringSlice("http.response.header."+strings.ToLower(name), values))
	}
	if _, cut := c.reqBody.savedBody(); cut {
		full = append(full, attribute.Bool("hotline.http.request.body.truncated", true))
	}
	if c.respBody != nil {
		if _, cut := c.respBody.savedBody(); cut {
			full = append(full, attribute.Bool("hotline.http.response.body.truncated", true))
		}
	}
	return full
}

// withBodies adds the kept part of each body, as bytes, which the file
// writes as base64.
func (c *call) withBodies(full []attribute.KeyValue) []attribute.KeyValue {
	if body, _ := c.reqBody.savedBody(); len(body) > 0 {
		full = append(full, attribute.ByteSlice("hotline.http.request.body", body))
	}
	if c.respBody != nil {
		if body, _ := c.respBody.savedBody(); len(body) > 0 {
			full = append(full, attribute.ByteSlice("hotline.http.response.body", body))
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
		Start: s.start, End: s.end, Failed: len(found) > 0, OK: len(found) == 0 && skipped == "",
	}
	wait := export.Span{
		ID: tracectx.NewSpanID(), Parent: check.ID, Name: "wait for check worker", Kind: trace.SpanKindInternal,
		Start: s.start, End: s.waited, Failed: s.dropped,
	}
	if s.dropped {
		wait.Attrs = []attribute.KeyValue{semconv.ErrorTypeKey.String("overloaded")}
	}
	return []export.Span{check, wait}
}

// refused records a request the proxy read but did not forward, with a
// minimal record and a trace of the call span alone. The vendor host is in
// it only when the request named one.
func (h *handler) refused(r *http.Request, start time.Time, ingress, id string, target *url.URL, status int) {
	attrs := []attribute.KeyValue{attribute.String("hotline.integration.id", orNotPassed(id)), clientAddress(r)}
	if ingress != "" {
		attrs = append(attrs, attribute.String("hotline.ingress", ingress))
	}
	if target != nil && target.Hostname() != "" {
		attrs = append(attrs, semconv.ServerAddress(strings.ToLower(target.Hostname())))
	}
	end := h.clock.Now()
	attrs = append(attrs,
		semconv.HTTPRequestMethodKey.String(r.Method),
		semconv.HTTPResponseStatusCode(status),
		semconv.ErrorTypeKey.String("http_request_error"),
		attribute.Int64("hotline.timing.total_us", end.Sub(start).Microseconds()),
	)
	traced, parent, _ := h.traced(id, r.Header, start)
	spans := []export.Span{callSpan(traced, parent, start, end, true)}
	h.record(r, finished{sc: traced, at: start, attrs: attrs, spans: spans})
}

// finished is what a call leaves: its record and its trace.
type finished struct {
	sc    trace.SpanContext
	at    time.Time
	attrs []attribute.KeyValue
	spans []export.Span
	// full, when set, is the record file exporters get instead of attrs.
	full []attribute.KeyValue
	// address is the address as the app sent it, for the console.
	address string
}

// record hands a record and its trace to the exporters, and measures the
// call for the proxy's own numbers.
func (h *handler) record(r *http.Request, f finished) {
	h.sink.Call(recordContext(f.sc), export.Call{Trace: f.sc, At: f.at, Attrs: f.attrs, Spans: f.spans, Full: f.full, Address: f.address})
	h.own.ended(f.attrs)
}

// clientAddress is the IP address the app's connection came from, never a
// header the app could set (Peter, 2026-10-31).
func clientAddress(r *http.Request) attribute.KeyValue {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return semconv.ClientAddress(host)
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
	redacted := c.api.Spec.Redact(c.route, spec.Call{
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
	if body := c.reqBody.signed(); body != "" {
		attrs = append(attrs, attribute.String("hotline.request.body.signature", body))
	}
	if c.resp != nil {
		if body := c.respBody.signed(); body != "" {
			attrs = append(attrs, attribute.String("hotline.response.body.signature", body))
		}
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
