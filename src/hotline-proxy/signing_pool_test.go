package proxy_test

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"

	proxy "hotline-proxy"
)

// A signer is reused between calls, with its keyed hash and lists. Reuse
// must leave nothing behind: signing the same record again after other
// records and traces gives the same signature as a signer that is new.
func TestReusedSignerLeavesNothingBehind(t *testing.T) {
	sc, at, attrs, spans := benchRecord()
	key := benchKey()
	pool := proxy.NewSigningPool(key)
	first := pool.Signature(attrs, at, sc)
	firstTrace := pool.TraceSignature(sc, spans)

	other := append([]attribute.KeyValue{attribute.String("aaa.first", "x")}, attrs[3:]...)
	_ = pool.Signature(other, at, sc)
	_ = pool.TraceSignature(sc, spans[:2])

	fresh := proxy.NewSigningPool(key)
	for name, got := range map[string][]attribute.KeyValue{
		"again":     pool.Signature(attrs, at, sc),
		"new":       fresh.Signature(attrs, at, sc),
		"old style": proxy.Signature(attrs, at, sc, key),
	} {
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("%s: record signature %v, want %v", name, got, first)
		}
	}
	if got := pool.TraceSignature(sc, spans); got != firstTrace {
		t.Fatalf("trace signature again: %v, want %v", got, firstTrace)
	}
	if got := fresh.TraceSignature(sc, spans); got != firstTrace {
		t.Fatalf("trace signature from a new signer: %v, want %v", got, firstTrace)
	}
}

// Two keys never share a signer.
func TestSigningPoolsKeepKeysApart(t *testing.T) {
	sc, at, attrs, _ := benchRecord()
	a := proxy.NewSigningPool([]byte("key-a")).Signature(attrs, at, sc)
	b := proxy.NewSigningPool([]byte("key-b")).Signature(attrs, at, sc)
	if a[1] == b[1] || a[0] == b[0] {
		t.Fatalf("two keys gave the same signature or key id: %v %v", a, b)
	}
}
