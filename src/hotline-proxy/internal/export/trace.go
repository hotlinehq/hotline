package export

import (
	"context"
	"crypto/tls"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials"

	"hotline-proxy/internal/config"
)

// Span is one step of a call, with its own start and end.
type Span struct {
	ID trace.SpanID
	// Parent is the span this one sits under; none for a span at the top
	// of the proxy's part of the trace.
	Parent trace.SpanID
	Name   string
	Kind   trace.SpanKind
	Start  time.Time
	End    time.Time
	Failed bool
	// OK marks a step that proved something went right, such as a check
	// that passed (Peter, 2026-10-31). Other spans leave it unset.
	OK    bool
	Attrs []attribute.KeyValue
}

// Trace queues the spans of one call, in the trace sc names, for every
// destination. It never blocks: a full queue drops its oldest span.
func (s *Sink) Trace(sc trace.SpanContext, spans []Span) {
	for _, d := range s.destinations {
		if d.wire != nil {
			d.wire.trace(sc, spans)
			continue
		}
		for _, span := range spans {
			d.spans.add(readOnly(sc, span, d))
		}
	}
}

// readOnly is a span as the trace SDK's senders take it.
func readOnly(sc trace.SpanContext, s Span, d destination) sdktrace.ReadOnlySpan {
	stub := tracetest.SpanStub{
		Name: s.Name,
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: sc.TraceID(), SpanID: s.ID, TraceFlags: sc.TraceFlags(),
		}),
		SpanKind:             s.Kind,
		StartTime:            s.Start,
		EndTime:              s.End,
		Attributes:           s.Attrs,
		Resource:             d.res,
		InstrumentationScope: instrumentation.Scope{Name: "hotline-proxy"},
	}
	if s.Parent.IsValid() {
		stub.Parent = trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: sc.TraceID(), SpanID: s.Parent, TraceFlags: sc.TraceFlags(), Remote: true,
		})
	}
	if s.Failed {
		stub.Status = sdktrace.Status{Code: codes.Error}
	}
	if s.OK {
		stub.Status = sdktrace.Status{Code: codes.Ok}
	}
	return stub.Snapshot()
}

// grpcTraces sends spans over gRPC.
func grpcTraces(e config.Exporter, tlsConfig *tls.Config, timing Timing) sdktrace.SpanExporter {
	compressor := "none"
	if e.Gzip {
		compressor = "gzip"
	}
	options := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(e.URL.Host),
		otlptracegrpc.WithHeaders(e.Headers),
		otlptracegrpc.WithCompressor(compressor),
		otlptracegrpc.WithTimeout(timing.Timeout),
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlptracegrpc.WithInsecure(),
	}
	if tlsConfig != nil {
		options[len(options)-1] = otlptracegrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig))
	}
	// New only starts the client, which connects on the first send.
	traces, _ := otlptracegrpc.New(context.Background(), options...)
	return traces
}

// httpTraces sends spans over HTTP as protobuf, to /v1/traces after the
// endpoint's own path.
func httpTraces(e config.Exporter, tlsConfig *tls.Config, timing Timing) sdktrace.SpanExporter {
	base := strings.TrimSuffix(e.URL.Path, "/")
	compression := otlptracehttp.NoCompression
	if e.Gzip {
		compression = otlptracehttp.GzipCompression
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(e.URL.Host),
		otlptracehttp.WithURLPath(base + "/v1/traces"),
		otlptracehttp.WithHeaders(e.Headers),
		otlptracehttp.WithCompression(compression),
		otlptracehttp.WithTimeout(timing.Timeout),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlptracehttp.WithTLSClientConfig(tlsConfig),
	}
	if tlsConfig == nil {
		options = append(options, otlptracehttp.WithInsecure())
	}
	traces, _ := otlptracehttp.New(context.Background(), options...)
	return traces
}
