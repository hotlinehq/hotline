package proxy_test

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
)

// benchRecord is what a full record of one call holds, about as many
// attributes and spans as the load test's small call makes.
func benchRecord() (trace.SpanContext, time.Time, []attribute.KeyValue, []export.Span) {
	at := time.Unix(1_700_000_000, 0)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	attrs := []attribute.KeyValue{
		attribute.String("hotline.integration.id", "load-test"),
		attribute.String("hotline.ingress", "proxy"),
		attribute.String("client.address", "127.0.0.1"),
		attribute.String("server.address", "api.load.test"),
		attribute.String("http.request.method", "PUT"),
		attribute.Int("http.response.status_code", 200),
		attribute.String("network.peer.address", "127.0.0.1"),
		attribute.Int64("hotline.timing.total_us", 812),
		attribute.String("url.scheme", "https"),
		attribute.Int("server.port", 8443),
		attribute.Int("http.request.body.size", 120),
		attribute.Bool("hotline.client.cancelled", false),
		attribute.String("hotline.config.version", "hotline-proxy.yaml+0123456789ab"),
		attribute.String("url.full", "https://api.load.test/v1/sessions/s-1?dry=true"),
		attribute.String("url.template", "/v1/sessions/{id}"),
		attribute.String("hotline.request.body.hash", "sha256:0000000000000000000000000000000000000000000000000000000000000000"),
		attribute.String("hotline.response.body.hash", "sha256:0000000000000000000000000000000000000000000000000000000000000000"),
		attribute.String("network.protocol.version", "1.1"),
		attribute.Int("http.response.body.size", 340),
		attribute.String("tls.protocol.version", "1.3"),
		attribute.StringSlice("http.request.header.content-type", []string{"application/json"}),
		attribute.StringSlice("http.response.header.content-type", []string{"application/json"}),
		attribute.String("hotline.schema.status", "valid"),
	}
	call := trace.SpanID{9, 9, 9, 9, 9, 9, 9, 9}
	spans := []export.Span{
		{ID: call, Name: "POST", Kind: trace.SpanKindServer, Start: at, End: at.Add(800 * time.Microsecond), Status: export.StatusOK,
			Attrs: []attribute.KeyValue{attribute.String("http.request.method", "PUT"), attribute.Int("http.response.status_code", 200)}},
		{ID: trace.SpanID{1, 1, 1, 1, 1, 1, 1, 1}, Parent: call, Name: "request spec check", Kind: trace.SpanKindInternal, Start: at, End: at.Add(300 * time.Microsecond), Status: export.StatusOK},
		{ID: trace.SpanID{2, 2, 2, 2, 2, 2, 2, 2}, Parent: trace.SpanID{1, 1, 1, 1, 1, 1, 1, 1}, Name: "wait for check worker", Kind: trace.SpanKindInternal, Start: at, End: at.Add(5 * time.Microsecond)},
		{ID: trace.SpanID{3, 3, 3, 3, 3, 3, 3, 3}, Parent: call, Name: "reply spec check", Kind: trace.SpanKindInternal, Start: at, End: at.Add(400 * time.Microsecond), Status: export.StatusOK},
		{ID: trace.SpanID{4, 4, 4, 4, 4, 4, 4, 4}, Parent: trace.SpanID{3, 3, 3, 3, 3, 3, 3, 3}, Name: "wait for check worker", Kind: trace.SpanKindInternal, Start: at, End: at.Add(5 * time.Microsecond)},
	}
	return sc, at, attrs, spans
}

// BenchmarkRecordHash is what hashing a record for the ledger costs, with
// one Hasher reused as the proxy does.
func BenchmarkRecordHash(b *testing.B) {
	sc, at, attrs, _ := benchRecord()
	r := ledger.Record{Attrs: attrs, At: at, TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String()}
	var h ledger.Hasher
	b.ReportAllocs()
	for b.Loop() {
		_ = h.Record(r)
	}
}

// BenchmarkTraceHash is what hashing a trace costs.
func BenchmarkTraceHash(b *testing.B) {
	sc, _, _, spans := benchRecord()
	steps := make([]ledger.Span, len(spans))
	for i, s := range spans {
		steps[i] = ledger.Span{ID: ledger.SpanID(s.ID), Name: s.Name, Kind: s.Kind.String(), Start: s.Start, End: s.End, Attrs: s.Attrs}
	}
	var h ledger.Hasher
	b.ReportAllocs()
	for b.Loop() {
		_ = h.Trace(sc.TraceID().String(), steps)
	}
}

// BenchmarkBodyHash is what hashing a small body costs.
func BenchmarkBodyHash(b *testing.B) {
	sc, _, _, _ := benchRecord()
	body := make([]byte, 340)
	var h ledger.Hasher
	b.ReportAllocs()
	for b.Loop() {
		bh := h.Body("response", sc.TraceID(), sc.SpanID())
		_, _ = bh.Write(body)
		_ = bh.Sum()
	}
}
