package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// signature signs everything a full record holds, so anyone with the record
// and the key can check that nobody changed it. The signed bytes are each
// attribute as an entry, sorted by name, then the record's timestamp, trace
// id, span id and flags (docs/proxy-signatures.md). The whole record is
// signed, so no list of names is needed (Peter, 2026-10-31).
func signature(attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext, key []byte) []attribute.KeyValue {
	sum := sha256.Sum256(key)
	keyID := attribute.String("hotline.signature_key_id", hex.EncodeToString(sum[:8]))
	// The exact size, so adding the key id does not copy the list again.
	sorted := make([]attribute.KeyValue, 0, len(attrs)+1)
	sorted = append(append(sorted, attrs...), keyID)
	slices.SortFunc(sorted, byKey)
	s := newSigner(key)
	for _, kv := range sorted {
		s.entry(kv)
	}
	traceID, spanID := sc.TraceID(), sc.SpanID()
	s.int("log.record.timestamp", at.UnixNano())
	s.hex("log.record.trace_id", traceID[:])
	s.hex("log.record.span_id", spanID[:])
	s.hex("log.record.trace_flags", []byte{byte(sc.TraceFlags())})
	return []attribute.KeyValue{keyID, attribute.String("hotline.signature", s.sum())}
}
