package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// finish ends the call once the response has gone back to the app. A side not
// yet checked, such as a request never sent, is checked now, when the call
// has a spec. The record and trace are made once the checks end, in the
// background, as for every call.
func (c *call) finish() {
	end := c.h.clock.Now()
	c.mu.Lock()
	switched := c.session != nil && !c.session.opened.IsZero()
	streamed := c.stream != nil && !c.stream.opened.IsZero()
	c.mu.Unlock()
	if streamed {
		// The call was recorded when its headers reached the app; what
		// ended now is the stream.
		c.h.recordLater(func() finished { return c.streamRecord(end) })
		return
	}
	if switched {
		// The call was recorded at its 101; what ended now is the session.
		// Its wires read what is left of its messages.
		c.session.endReading()
		c.h.recordLater(func() finished { return c.sessionRecord(end) })
		return
	}
	if c.api != nil {
		c.checkRequest(end)
		c.mu.Lock()
		resp := c.resp
		c.mu.Unlock()
		c.checkResponse(end, resp)
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

// failure is how the call failed: as fail noted, or, for a response the
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
	responded := c.responseCheck.wait()
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
		req, response := c.reqCheck.found, c.responseCheck.found
		requested := c.reqCheck.started.Load()
		attrs = c.full(attrs, req.With(response), requested || responded)
		// Each side was checked alongside the call, so its step may end
		// after the call span (Peter, 2026-10-31).
		if requested {
			spans = append(spans, c.checkSpans("request spec check", c.trace.SpanID(), c.reqCheck, req.Request, req.Skipped)...)
		}
		if responded {
			spans = append(spans, c.checkSpans("response spec check", c.trace.SpanID(), c.responseCheck, response.Response, response.Skipped)...)
		}
	} else {
		// A minimal record carries the sizes too, counted as the bodies
		// passed and never kept (Peter, 2026-11-08). The response's size is there
		// only when a response came.
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
	attrs = probeAttrs(attrs, c.h.probes.noteCall(c.probeTarget(), c.outcome(failed), c.trace), failed.errorType != "")
	// Every body that passed has its hash, minimal record or full, an
	// empty one too, so each is in the ledger (Peter, 2026-11-16).
	attrs = append(attrs, attribute.String("hotline.request.body.hash", c.reqBody.hashed()))
	response := c.h.emptyBodyHash("response", c.trace)
	if c.respBody != nil {
		response = c.respBody.hashed()
	}
	attrs = append(attrs, attribute.String("hotline.response.body.hash", response))
	var content []attribute.KeyValue
	if c.integration.Content.Full {
		content = c.content()
		attrs = append(attrs, attribute.String("hotline.content.hash", ledger.Text(c.h.hashes.Content(content))))
		content = c.withBodies(content)
	}
	return finished{cfg: c.cfg, chain: c.integration.Name, sc: c.trace, at: c.start, attrs: attrs, spans: spans, content: content}
}

// probeTarget is the call's vendor endpoint, as a probe of it needs it.
func (c *call) probeTarget() probeTarget {
	return probeTarget{host: c.host, port: c.port, tls: c.scheme == "https", allowInternal: c.transport == c.h.internal}
}

// outcome is what the call tells the probes of its endpoint: it failed to
// connect, it connected, or it never tried.
func (c *call) outcome(failed failure) callOutcome {
	switch {
	case connectFailure(failed.errorType):
		return failedToConnect
	case c.peer != "":
		return connected
	}
	return neverTried
}

// content is what content: full adds to the record for the file
// exporters: url.full as sent and every header of the call and its response,
// under hotline.content.* names, beside the record, which stays as its
// hash covers it (Peter, 2026-11-16). Each value that is secret, by the
// spec or always (Authorization, Proxy-Authorization, Cookie,
// Set-Cookie), is REDACTED, so no key is copied out of a file kept for
// debugging (Peter, 2026-11-18). A body cut at maxBodySize is flagged.
func (c *call) content() []attribute.KeyValue {
	var response http.Header
	if c.resp != nil {
		response = c.resp.Header
	}
	var api *spec.Spec
	if c.api != nil {
		api = c.api.Spec
	}
	hidden := spec.Hide(api, c.specRoute(), spec.Call{
		EscapedPath:     c.target.EscapedPath(),
		RawQuery:        c.target.RawQuery,
		Header:          c.in.Header,
		ResponseHeaders: response,
	})
	full := make([]attribute.KeyValue, 0, len(hidden.Header)+len(hidden.ResponseHeaders)+5)
	full = append(full, attribute.String("hotline.content.url.full", c.hiddenAddress(hidden)))
	for name, values := range hidden.Header {
		full = append(full, attribute.StringSlice("hotline.content.request.header."+strings.ToLower(name), values))
	}
	for name, values := range hidden.ResponseHeaders {
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

// hiddenAddress is the address as sent, with the path and query that
// spec.Hide left, so its secrets are REDACTED.
func (c *call) hiddenAddress(hidden spec.Call) string {
	out := c.sentURL()
	out.Path, _ = url.PathUnescape(hidden.EscapedPath)
	out.RawPath, out.RawQuery = hidden.EscapedPath, hidden.RawQuery
	return out.String()
}

// sentAddress is the address the call went to, as the app sent it: the scheme
// the proxy used, no user and password, the path and query as written.
func (c *call) sentAddress() string {
	out := c.sentURL()
	return out.String()
}

// sentURL is the address the call went to, as a value to change.
func (c *call) sentURL() url.URL {
	out := *c.target
	out.Scheme, out.User, out.Fragment = c.scheme, nil, ""
	out.Host = c.host
	if c.target.Port() != "" {
		out.Host = net.JoinHostPort(c.host, c.port)
	}
	return out
}

// checkSpans are the step of one side's check, under parent, error when that side broke
// the spec, ok when it passed, unset when part of it was skipped, and under
// it the wait for a free worker. The wait is an error with error.type
// overloaded when the check gave way in a full line (Peter, 2026-11-02).
func (c *call) checkSpans(name string, parent trace.SpanID, s *specCheck, found []spec.Finding, skipped string) []export.Span {
	check := export.Span{
		ID: tracectx.NewSpanID(), Parent: parent, Name: name, Kind: trace.SpanKindInternal,
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
			attribute.String("hotline.request.body.hash", h.emptyBodyHash("request", traced)),
			attribute.String("hotline.response.body.hash", h.emptyBodyHash("response", traced)))
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
	// event is the record's event name: empty for a call,
	// export.SessionEvent for a WebSocket session, or export.MessageEvent
	// for one of its messages.
	event string
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
	// body is the record's text: a probe's verdict in a sentence. Calls
	// have none.
	body  string
	spans []export.Span
	// content, under content: full, is what file exporters get besides the
	// record.
	content []attribute.KeyValue
}

// recordLater is the one way every call is recorded: in the background,
// apart from the call, so the app's connection is free as soon as its response
// is sent (Peter, 2026-11-12). build makes the record and trace, which are
// then handed to the exporters and measured for the proxy's own numbers.
// Shutdown waits for it, as for a call in flight. It is only called while
// the call itself is still counted in flight.
func (h *handler) recordLater(build func() finished) {
	h.recordThen(build, nil)
}

// recordThen is recordLater, and then, when set, runs once the record is
// handed on, so the records that must come after it can wait for it.
func (h *handler) recordThen(build func() finished, then func()) {
	h.calls.Add(1)
	go func() {
		defer h.calls.Done()
		f := build()
		f.validText()
		h.book.record(recordContext(f.sc), f)
		if f.event == "" {
			// Only a call has a total time to measure.
			h.own.measureCall(f.attrs)
		}
		if then != nil {
			then()
		}
	}()
}

// validText makes every text of the record and its trace valid UTF-8,
// before the ledger hashes it, so every exporter and the hash see the same
// text. A header or address that an app or a vendor sent may hold any
// bytes, and OTLP text must be UTF-8: a collector refuses the whole send
// otherwise, losing every record in it (found by fuzzing, 2026-11-18). Each
// byte that is not UTF-8 becomes the replacement character.
func (f *finished) validText() {
	validAttrs(f.attrs)
	validAttrs(f.content)
	for i := range f.spans {
		f.spans[i].Name = strings.ToValidUTF8(f.spans[i].Name, "\uFFFD")
		validAttrs(f.spans[i].Attrs)
	}
}

// validAttrs makes the text values in attrs valid UTF-8, in place. Values
// that already are, nearly all of them, are left as they are.
func validAttrs(attrs []attribute.KeyValue) {
	for i, kv := range attrs {
		switch kv.Value.Type() {
		case attribute.STRING:
			if s := kv.Value.AsString(); !utf8.ValidString(s) {
				attrs[i] = attribute.String(string(kv.Key), strings.ToValidUTF8(s, "\uFFFD"))
			}
		case attribute.STRINGSLICE:
			list := kv.Value.AsStringSlice()
			if !slices.ContainsFunc(list, func(s string) bool { return !utf8.ValidString(s) }) {
				continue
			}
			for j, s := range list {
				list[j] = strings.ToValidUTF8(s, "\uFFFD")
			}
			attrs[i] = attribute.StringSlice(string(kv.Key), list)
		}
	}
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
//
// Both times are cut to whole microseconds first, as the ledger cuts every
// span time before it is hashed and sent, so the length matches the span
// as sent (2026-11-25).
func micros(from, to time.Time) int64 {
	return to.UnixMicro() - from.UnixMicro()
}

func orNotPassed(id string) string {
	if id == "" {
		return "not-passed"
	}
	return id
}

// full adds the rest of the record to attrs, for calls with a known
// integration and api, and the status of the check against the spec, which
// is part of it.
func (c *call) full(attrs []attribute.KeyValue, found spec.Checked, ran bool) []attribute.KeyValue {
	origin := c.scheme + "://" + c.host
	if c.target.Port() != "" {
		origin += ":" + c.port
	}
	var responseHeaders map[string][]string
	if c.resp != nil {
		responseHeaders = c.resp.Header
	}
	// record waited for both checks, so the route is found by now.
	redacted := c.api.Spec.Redact(c.specRoute(), spec.Call{
		Origin:          origin,
		EscapedPath:     c.target.EscapedPath(),
		RawQuery:        c.target.RawQuery,
		Header:          c.in.Header,
		ResponseHeaders: responseHeaders,
	})
	port, _ := strconv.Atoi(c.port)
	sent, _ := c.reqBody.counts()
	// Room for the headers and found fields at once, in the record's own list.
	attrs = slices.Grow(attrs, 16+len(redacted.Header)+len(redacted.ResponseHeaders)+len(found.Copied))
	attrs = append(attrs,
		semconv.URLScheme(c.scheme),
		semconv.ServerPort(port),
		semconv.HTTPRequestBodySize(int(sent)),
		attribute.Bool("hotline.client.cancelled", c.appFailed()),
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
	for name, values := range redacted.ResponseHeaders {
		attrs = append(attrs, attribute.StringSlice("http.response.header."+name, values))
	}
	// The file parts come from the reading of the body, so they are listed
	// even when its check gave way in a full line.
	if files := c.api.Spec.RecordFiles(c.specRoute(), c.in.Header.Get("Content-Type"), c.reqBody.formFiles()); files != nil {
		attrs = append(attrs, filesAttr(files))
	}
	return checked(attrs, found, ran)
}

// shownFindings is how many findings of one side a record holds at most.
const shownFindings = 20

// checked is what the checks found, as the record holds it, with the
// status: valid, invalid, or skipped when nothing was found but part of
// the check did not run (Peter, 2026-10-31).
func checked(attrs []attribute.KeyValue, found spec.Checked, ran bool) []attribute.KeyValue {
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
	if len(found.Request) > 0 || len(found.Response) > 0 {
		status = "invalid"
	}

	// A fixed pair, not a map built for every call.
	sides := [2]struct {
		name     string
		findings []spec.Finding
	}{{"request", found.Request}, {"response", found.Response}}
	for _, one := range sides {
		side, findings := one.name, one.findings
		if len(findings) == 0 {
			continue
		}
		key := "hotline.schema." + side + ".errors"
		if len(findings) > shownFindings {
			attrs = append(attrs, attribute.Int(key+"_total", len(findings)))
			findings = findings[:shownFindings]
		}
		values := make([]attribute.Value, len(findings))
		for i, r := range findings {
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

// filesAttr lists the file parts of a multipart request body: each one's
// name in the form, size, content type and file name, which is REDACTED
// unless the spec marks it x-hotline-redact false (Peter, 2026-11-24, M4,
// Q4 and Q5). The file itself is never read.
func filesAttr(files []spec.RecordedFile) attribute.KeyValue {
	values := make([]attribute.Value, len(files))
	for i, f := range files {
		values[i] = attribute.MapValue(
			attribute.String("name", f.Name),
			attribute.Int64("size", f.Size),
			attribute.String("content_type", f.ContentType),
			attribute.String("file_name", f.FileName),
		)
	}
	return attribute.Slice("hotline.request.files", values...)
}
