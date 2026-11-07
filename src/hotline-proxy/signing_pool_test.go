package proxy_test

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"

	proxy "hotline-proxy"
)

// A signer is reused between calls, with its buffer and lists. Reuse must
// leave nothing behind: signing the same record again after other records
// and traces gives the same signature as a signer that is new.
func TestReusedSignerLeavesNothingBehind(t *testing.T) {
	sc, at, attrs, spans := benchRecord()
	key := benchKey()
	pool := proxy.NewSigningPool()
	first := pool.Signature(key, attrs, at, sc)
	firstTrace := pool.TraceSignature(key, sc, spans)

	other := append([]attribute.KeyValue{attribute.String("aaa.first", "x")}, attrs[3:]...)
	_ = pool.Signature(key, other, at, sc)
	_ = pool.TraceSignature(key, sc, spans[:2])

	fresh := proxy.NewSigningPool()
	for name, got := range map[string][]attribute.KeyValue{
		"again":     pool.Signature(key, attrs, at, sc),
		"new":       fresh.Signature(key, attrs, at, sc),
		"old style": proxy.Signature(attrs, at, sc, key),
	} {
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("%s: record signature %v, want %v", name, got, first)
		}
	}
	if got := pool.TraceSignature(key, sc, spans); got != firstTrace {
		t.Fatalf("trace signature again: %v, want %v", got, firstTrace)
	}
	if got := fresh.TraceSignature(key, sc, spans); got != firstTrace {
		t.Fatalf("trace signature from a new signer: %v, want %v", got, firstTrace)
	}
}

// One pool serves every integration: each signature uses only its own key,
// and a key used before does not stay in a reused signer.
func TestOnePoolKeepsKeysApart(t *testing.T) {
	sc, at, attrs, spans := benchRecord()
	pool := proxy.NewSigningPool()
	a := pool.Signature([]byte("key-a"), attrs, at, sc)
	b := pool.Signature([]byte("key-b"), attrs, at, sc)
	if a[1] == b[1] || a[0] == b[0] {
		t.Fatalf("two keys gave the same signature or key id: %v %v", a, b)
	}
	if again := pool.Signature([]byte("key-a"), attrs, at, sc); again[1] != a[1] {
		t.Fatalf("key a after key b: %v, want %v", again, a)
	}
	ta := pool.TraceSignature([]byte("key-a"), sc, spans)
	if tb := pool.TraceSignature([]byte("key-b"), sc, spans); ta == tb {
		t.Fatal("two keys gave the same trace signature")
	}
}
