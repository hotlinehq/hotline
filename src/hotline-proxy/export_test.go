package proxy

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
)

// What the tests in proxy_test may reach: signing, to benchmark it and to
// pin that it does not depend on the order of attributes or on reuse.

// SigningPool is the pool of signers the proxy signs with.
type SigningPool struct{ p *signPool }

// NewSigningPool is an empty signPool.
func NewSigningPool() SigningPool { return SigningPool{&signPool{}} }

// Signature signs a record.
func (s SigningPool) Signature(key []byte, attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext) []attribute.KeyValue {
	return s.p.signature(key, attrs, at, sc)
}

// TraceSignature signs a trace.
func (s SigningPool) TraceSignature(key []byte, sc trace.SpanContext, spans []export.Span) attribute.KeyValue {
	return s.p.traceSignature(key, sc, spans)
}

// Signature signs a record with a signer that is new.
func Signature(attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext, key []byte) []attribute.KeyValue {
	return (&signPool{}).signature(key, attrs, at, sc)
}

// TraceSignature signs a trace with a signer that is new.
func TraceSignature(sc trace.SpanContext, spans []export.Span, key []byte) attribute.KeyValue {
	return (&signPool{}).traceSignature(key, sc, spans)
}
