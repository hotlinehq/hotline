package proxy

import (
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
)

// What the tests in proxy_test may reach: signing, to benchmark it and to
// pin that it does not depend on the order of attributes or on reuse.

// SigningPool is a pool of signers for one key.
type SigningPool struct{ p *signPool }

// NewSigningPool is newSignPool.
func NewSigningPool(key []byte) SigningPool { return SigningPool{newSignPool(key)} }

// Signature signs a record.
func (s SigningPool) Signature(attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext) []attribute.KeyValue {
	return s.p.signature(attrs, at, sc)
}

// TraceSignature signs a trace.
func (s SigningPool) TraceSignature(sc trace.SpanContext, spans []export.Span) attribute.KeyValue {
	return s.p.traceSignature(sc, spans)
}

// Signature signs a record with a signer that is new.
func Signature(attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext, key []byte) []attribute.KeyValue {
	return newSignPool(key).signature(attrs, at, sc)
}

// TraceSignature signs a trace with a signer that is new.
func TraceSignature(sc trace.SpanContext, spans []export.Span, key []byte) attribute.KeyValue {
	return newSignPool(key).traceSignature(sc, spans)
}
