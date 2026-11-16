package proxy

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/tracectx"
)

// opened is when the call's time starts: when it reached the proxy or, on
// the first call of an app's TLS connection, when TLS with the app started
// to be set up (Peter, 2026-10-31).
func (c *call) opened() time.Time {
	if c.appTimed {
		return c.appStart
	}
	return c.start
}

// The names of a call's steps, as its spans show them. step names the step
// a call failed in with these same names, and spans marks the span of that
// name as failed, so a renamed step stays marked.
const (
	stepAppTLS    = "app tls handshake"
	stepRouting   = "routing"
	stepDNS       = "dns lookup"
	stepConnect   = "tcp connect"
	stepVendorTLS = "vendor tls handshake"
	stepSend      = "send call"
	stepWait      = "wait for vendor"
	stepReceive   = "receive reply"
	stepReply     = "reply to app"
)

// phaseStep is a step of the vendor connection, with the phase it is.
type phaseStep struct {
	phase phase
	name  string
}

// phaseSteps are the steps of the vendor connection, in order.
func phaseSteps() [3]phaseStep {
	return [3]phaseStep{{phaseDNS, stepDNS}, {phaseConnect, stepConnect}, {phaseTLS, stepVendorTLS}}
}

// spans are the steps of the call, as its trace (Peter, 2026-10-31). A step
// that did not happen has no span. Spans hold nothing the record holds, so
// only two of them have an attribute.
func (c *call) spans(opened, end time.Time, failed failure) []export.Span {
	call := callSpan(c.trace, c.parent, opened, end, failed.errorType != "" || c.app.failed())
	spans := []export.Span{call}
	step := func(parent trace.SpanID, name string, from, to time.Time, attrs ...attribute.KeyValue) {
		spans = append(spans, export.Span{
			ID: tracectx.NewSpanID(), Parent: parent, Name: name, Kind: trace.SpanKindInternal,
			Start: from, End: to, Status: export.FailedIf(name == failed.step), Attrs: attrs,
		})
	}
	if c.appTimed {
		step(call.ID, stepAppTLS, c.appStart, c.appEnd)
	}
	step(call.ID, stepRouting, c.start, c.vendorIn)
	// The reply ends when the vendor's reply ended, or when the call
	// failed.
	out := orTime(c.vendorOut, end)
	vendor := export.Span{
		ID: c.vendorSpan, Parent: call.ID, Name: "vendor call", Kind: trace.SpanKindClient,
		Start: c.vendorIn, End: out, Status: export.FailedIf(failed.errorType != ""),
	}
	if c.connected {
		vendor.Attrs = []attribute.KeyValue{attribute.Bool("hotline.connection.reused", c.reused)}
	}
	spans = append(spans, vendor)
	for _, p := range phaseSteps() {
		t := c.times[p.phase]
		if t[0].IsZero() {
			continue
		}
		if t[1].IsZero() {
			t[1] = out
		}
		var attrs []attribute.KeyValue
		if p.phase == phaseDNS && c.looked != "" {
			attrs = append(attrs, semconv.DNSQuestionName(c.looked))
		}
		step(vendor.ID, p.name, t[0], t[1], attrs...)
	}
	if !c.gotAt.IsZero() {
		step(vendor.ID, stepSend, c.gotAt, orTime(c.wrote, out))
	}
	if !c.wrote.IsZero() && (c.firstByte.IsZero() || !c.firstByte.Before(c.wrote)) {
		step(vendor.ID, stepWait, c.wrote, orTime(c.firstByte, out))
	}
	if !c.firstByte.IsZero() {
		step(vendor.ID, stepReceive, c.firstByte, out)
	}
	step(call.ID, stepReply, out, end)
	// The app hung up: the vendor did nothing wrong, so only the last step
	// fails (Peter, 2026-10-31).
	spans[len(spans)-1].Status = export.FailedIf(c.app.failed())
	return spans
}

// orTime is t, or instead when t is not set.
func orTime(t, instead time.Time) time.Time {
	if t.IsZero() {
		return instead
	}
	return t
}

// callSpan is the span of the whole call, under the app's span when the app
// sent one. Its id is the record's span id, which links the two.
func callSpan(sc trace.SpanContext, parent trace.SpanID, from, to time.Time, failed bool) export.Span {
	return export.Span{
		ID: sc.SpanID(), Parent: parent, Name: "hotline call", Kind: trace.SpanKindServer,
		Start: from, End: to, Status: export.FailedIf(failed),
	}
}
