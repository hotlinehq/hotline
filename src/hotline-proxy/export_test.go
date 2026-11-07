package proxy

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
)

// What the tests in proxy_test may reach: signing, to benchmark it and to
// pin that it does not depend on the order of attributes.

// Signature is signature.
func Signature(attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext, key []byte) []attribute.KeyValue {
	return signature(attrs, at, sc, key)
}

// TraceSignature is traceSignature.
func TraceSignature(sc trace.SpanContext, spans []export.Span, key []byte) attribute.KeyValue {
	return traceSignature(sc, spans, key)
}
