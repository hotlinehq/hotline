package proxy

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/export"
)

// A span's attributes are signed sorted by name, so the signature does not
// depend on the order they were added in, and a verifier can rebuild it from
// the trace in any order.
func TestTraceSignatureIgnoresAttributeOrder(t *testing.T) {
	sc, _, _, spans := benchRecord()
	a := []export.Span{spans[0]}
	b := []export.Span{spans[0]}
	a[0].Attrs = []attribute.KeyValue{attribute.String("a", "1"), attribute.String("b", "2"), attribute.String("c", "3")}
	b[0].Attrs = []attribute.KeyValue{attribute.String("c", "3"), attribute.String("a", "1"), attribute.String("b", "2")}
	if got, want := traceSignature(sc, b, benchKey), traceSignature(sc, a, benchKey); got != want {
		t.Fatalf("signature depends on attribute order: %v, want %v", got, want)
	}
	b[0].Attrs[0] = attribute.String("c", "changed")
	if traceSignature(sc, b, benchKey) == traceSignature(sc, a, benchKey) {
		t.Fatal("a changed attribute did not change the signature")
	}
}
