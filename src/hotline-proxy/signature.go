package proxy

import (
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
func (p *signPool) signature(key []byte, attrs []attribute.KeyValue, at time.Time, sc trace.SpanContext) []attribute.KeyValue {
	borrowed := p.get(key)
	defer p.put(borrowed)
	id := keyID(key)
	borrowed.sorted = append(append(borrowed.sorted[:0], attrs...), id)
	slices.SortFunc(borrowed.sorted, byKey)
	s := &borrowed.s
	for _, kv := range borrowed.sorted {
		s.entry(kv)
	}
	traceID, spanID := sc.TraceID(), sc.SpanID()
	s.int("log.record.timestamp", at.UnixNano())
	s.hex("log.record.trace_id", traceID[:])
	s.hex("log.record.span_id", spanID[:])
	s.hex("log.record.trace_flags", []byte{byte(sc.TraceFlags())})
	return []attribute.KeyValue{id, attribute.String("hotline.signature", borrowed.sum())}
}
