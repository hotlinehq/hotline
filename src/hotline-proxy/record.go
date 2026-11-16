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
	return finished{cfg: c.cfg, sc: c.trace, at: c.start, attrs: attrs, spans: spans, content: content}
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
	cfg := h.cfg.Load()
	traced, parent, _ := traceOf(cfg.Integration(id), r.Header, start)
	// No body was read, so each side's hash is of zero bytes.
	attrs = append(attrs,
		attribute.String("hotline.request.body.hash", emptyBodyHash("request", traced)),
		attribute.String("hotline.response.body.hash", emptyBodyHash("response", traced)))
	spans := []export.Span{callSpan(traced, parent, start, end, true)}
	h.recordLater(recorded(finished{cfg: cfg, sc: traced, at: start, attrs: attrs, spans: spans}))
}

// finished is what a call leaves: its record and its trace.
type finished struct {
	// cfg is the config the call started with. The ledger puts the record
	// in a batch of that config, even when a reload came before the
	// record was made (Peter, 2026-11-16).
	cfg   *config.Config
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
