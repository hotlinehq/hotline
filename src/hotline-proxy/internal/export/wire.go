package export

// The proxy writes its two busiest messages, the call record and the call's
// spans, straight into OTLP protobuf bytes (docs/otlp-encoder-scope.md).
// Each is written once, for every exporter, and a send only places the
// written items behind a short header. Field numbers are those of
// opentelemetry-proto: logs/v1, trace/v1, common/v1 and resource/v1.

import (
	"hotline-proxy/internal/fits"
	"math"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protowire"
)

// openField starts a field of a message or bytes with a one-byte length,
// returning where its content starts. closeField writes the real length.
func openField(b []byte, field protowire.Number) ([]byte, int) {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = append(b, 0)
	return b, len(b)
}

// closeField writes the length of the field opened at start, moving its
// content on when the length takes more than one byte.
func closeField(b []byte, start int) []byte {
	n := len(b) - start
	length := uint64(len(b[start:]))
	if length < 0x80 {
		b[start-1] = byte(length)
		return b
	}
	size := protowire.SizeVarint(length)
	b = append(b, make([]byte, size-1)...)
	copy(b[start+size-1:], b[start:start+n])
	protowire.AppendVarint(b[start-1:start-1], length)
	return b
}

// Only appendFixed32 leaves out a zero, the flags of a call the app did not
// sample; every other value the proxy writes is set.

func appendText(b []byte, field protowire.Number, s string) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendBytes(b []byte, field protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func appendFixed64(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, v)
}

func appendFixed32(b []byte, field protowire.Number, v uint32) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.Fixed32Type)
	return protowire.AppendFixed32(b, v)
}

func appendVarint(b []byte, field protowire.Number, v uint64) []byte {
	b = protowire.AppendTag(b, field, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// nanos is t in nanoseconds since 1970, 0 for a time before it.
func nanos(t time.Time) uint64 {
	return uint64(max(0, t.UnixNano()))
}

// appendKeyValue writes one KeyValue message as field.
func appendKeyValue(b []byte, field protowire.Number, kv attribute.KeyValue) []byte {
	b, start := openField(b, field)
	b = appendText(b, 1, string(kv.Key))
	b = appendValue(b, 2, kv.Value)
	return closeField(b, start)
}

// appendValue writes one AnyValue message as field: a text, a whole
// number, a number, true or false, a list of texts, a list or a map. The
// proxy records no other kind.
func appendValue(b []byte, field protowire.Number, v attribute.Value) []byte {
	b, start := openField(b, field)
	switch v.Type() {
	case attribute.STRING:
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, v.AsString())
	case attribute.BOOL:
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, protowire.EncodeBool(v.AsBool()))
	case attribute.INT64:
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(v.AsInt64())) //nolint:gosec // protobuf writes an int64 as its bits
	case attribute.FLOAT64:
		b = protowire.AppendTag(b, 4, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, math.Float64bits(v.AsFloat64()))
	case attribute.STRINGSLICE:
		b = appendList(b, v.AsStringSlice(), attribute.StringValue)
	case attribute.SLICE:
		b = appendList(b, v.AsSlice(), func(item attribute.Value) attribute.Value { return item })
	case attribute.MAP:
		var list int
		b, list = openField(b, 6)
		for _, kv := range v.AsMap() {
			b = appendKeyValue(b, 1, kv)
		}
		b = closeField(b, list)
	}
	return closeField(b, start)
}

// appendList writes an ArrayValue, the items of an AnyValue list.
func appendList[T any](b []byte, items []T, value func(T) attribute.Value) []byte {
	b, start := openField(b, 5)
	for _, item := range items {
		b = appendValue(b, 1, value(item))
	}
	return closeField(b, start)
}

// appendRecord writes the content of one LogRecord message, without its
// own tag and length, so it fits any batch. A record with no body, every
// one but a probe's, leaves the body out.
func appendRecord(b []byte, event string, at, observed time.Time, severity log.Severity, sc trace.SpanContext, body string, attrs []attribute.KeyValue) []byte {
	b = appendFixed64(b, 1, nanos(at))
	level, _ := fits.Uint64(int(severity)) // a negative level is not one OTLP knows, so it goes as 0, unspecified
	b = appendVarint(b, 2, level)
	b = appendText(b, 3, severity.String())
	if body != "" {
		b = appendValue(b, 5, attribute.StringValue(body))
	}
	for _, kv := range attrs {
		b = appendKeyValue(b, 6, kv)
	}
	if sc.IsValid() {
		b = appendFixed32(b, 8, uint32(sc.TraceFlags()))
	}
	if id := sc.TraceID(); id.IsValid() {
		b = appendBytes(b, 9, id[:])
	}
	if id := sc.SpanID(); id.IsValid() {
		b = appendBytes(b, 10, id[:])
	}
	b = appendFixed64(b, 11, nanos(observed))
	return appendText(b, 12, event)
}

// appendSpan writes the content of one Span message, without its own tag
// and length.
func appendSpan(b []byte, sc trace.SpanContext, s Span) []byte {
	traceID := sc.TraceID()
	b = appendBytes(b, 1, traceID[:])
	b = appendBytes(b, 2, s.ID[:])
	if s.Parent.IsValid() {
		b = appendBytes(b, 4, s.Parent[:])
	}
	b = appendText(b, 5, s.Name)
	kind, _ := fits.Uint64(int(s.Kind)) // numbered as in OTLP; a negative kind goes as 0, unspecified
	b = appendVarint(b, 6, kind)
	b = appendFixed64(b, 7, nanos(s.Start))
	b = appendFixed64(b, 8, nanos(s.End))
	for _, kv := range s.Attrs {
		b = appendKeyValue(b, 9, kv)
	}
	for _, link := range s.Links {
		var start int
		b, start = openField(b, 13)
		traceID, spanID := link.TraceID(), link.SpanID()
		b = appendBytes(b, 1, traceID[:])
		b = appendBytes(b, 2, spanID[:])
		b = appendFixed32(b, 6, linkFlags(link))
		b = closeField(b, start)
	}
	var status int
	b, status = openField(b, 15)
	// OTLP numbers the statuses as Status does: unset 0, ok 1, error 2.
	// Unset is written as nothing.
	if s.Status != StatusUnset {
		code, _ := fits.Uint64(int(s.Status)) // a status is 1 or 2
		b = appendVarint(b, 3, code)
	}
	b = closeField(b, status)
	return appendFixed32(b, 16, s.flags(sc))
}

// resourceOf writes the content of a Resource message.
func resourceOf(attrs []attribute.KeyValue) []byte {
	var b []byte
	for _, kv := range attrs {
		b = appendKeyValue(b, 1, kv)
	}
	return b
}

// appendBatch writes a whole export request from the items' own content:
// one resource, one scope named hotline-proxy, and the items one after
// another. ExportLogsServiceRequest and ExportTraceServiceRequest have the
// same field numbers down to the items, so one function writes both.
func appendBatch(b, resource []byte, items [][]byte) []byte {
	b, outer := openField(b, 1) // resource_logs or resource_spans
	b = appendBytes(b, 1, resource)
	b, inner := openField(b, 2) // scope_logs or scope_spans
	b, scope := openField(b, 1)
	b = appendText(b, 1, "hotline-proxy")
	b = closeField(b, scope)
	for _, item := range items {
		b = appendBytes(b, 2, item)
	}
	b = closeField(b, inner)
	return closeField(b, outer)
}
