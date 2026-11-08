package export

// The proxy writes its two busiest messages, the call record and the call's
// spans, straight into OTLP protobuf bytes (docs/otlp-encoder-scope.md).
// Each is written once, for every destination, and a send only places the
// written items behind a short header. Field numbers are those of
// opentelemetry-proto: logs/v1, trace/v1, common/v1 and resource/v1.

import (
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
	if n < 0x80 {
		b[start-1] = byte(n) //nolint:gosec // below 0x80, checked above
		return b
	}
	size := protowire.SizeVarint(uint64(n))
	b = append(b, make([]byte, size-1)...)
	copy(b[start+size-1:], b[start:start+n])
	protowire.AppendVarint(b[start-1:start-1], uint64(n))
	return b
}

// The append functions leave out a value that is zero, as protobuf does.

func appendText(b []byte, field protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendBytes(b []byte, field protowire.Number, v []byte) []byte {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func appendFixed64(b []byte, field protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
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
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, field, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// nanos is t in nanoseconds since 1970, 0 for a time before it.
func nanos(t time.Time) uint64 {
	if n := t.UnixNano(); n > 0 {
		return uint64(n)
	}
	return 0
}

// appendKeyValue writes one KeyValue message as field.
func appendKeyValue(b []byte, field protowire.Number, kv attribute.KeyValue) []byte {
	b, start := openField(b, field)
	b = appendText(b, 1, string(kv.Key))
	b = appendValue(b, 2, kv.Value)
	return closeField(b, start)
}

// appendValue writes one AnyValue message as field: a text, a whole
// number, a number, true or false, bytes, a list or a map.
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
	case attribute.BYTESLICE:
		b = appendBytes(b, 7, v.AsByteSlice())
	case attribute.STRINGSLICE:
		b = appendList(b, v.AsStringSlice(), attribute.StringValue)
	case attribute.INT64SLICE:
		b = appendList(b, v.AsInt64Slice(), attribute.Int64Value)
	case attribute.FLOAT64SLICE:
		b = appendList(b, v.AsFloat64Slice(), attribute.Float64Value)
	case attribute.BOOLSLICE:
		b = appendList(b, v.AsBoolSlice(), attribute.BoolValue)
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
// own tag and length, so it fits any batch.
func appendRecord(b []byte, at, observed time.Time, severity log.Severity, sc trace.SpanContext, attrs []attribute.KeyValue) []byte {
	b = appendFixed64(b, 1, nanos(at))
	b = appendVarint(b, 2, uint64(severity)) //nolint:gosec // a level is a small positive number
	b = appendText(b, 3, severity.String())
	for _, kv := range attrs {
		b = appendKeyValue(b, 6, kv)
	}
	b = appendFixed32(b, 8, uint32(sc.TraceFlags()))
	if id := sc.TraceID(); id.IsValid() {
		b = appendBytes(b, 9, id[:])
	}
	if id := sc.SpanID(); id.IsValid() {
		b = appendBytes(b, 10, id[:])
	}
	b = appendFixed64(b, 11, nanos(observed))
	return appendText(b, 12, "hotline.request")
}

// The span flags of OTLP besides the W3C trace flags: that the span says
// whether its parent is remote, and that it is.
const (
	flagHasIsRemote = 0x100
	flagIsRemote    = 0x200
)

// appendSpan writes the content of one Span message, without its own tag
// and length. Only the call span's parent, the app's span, ran in another
// program, so only it is remote (Peter, 2026-11-08).
func appendSpan(b []byte, sc trace.SpanContext, s Span) []byte {
	traceID := sc.TraceID()
	b = appendBytes(b, 1, traceID[:])
	b = appendBytes(b, 2, s.ID[:])
	flags := uint32(sc.TraceFlags()) | flagHasIsRemote
	if s.Parent.IsValid() {
		b = appendBytes(b, 4, s.Parent[:])
		if s.ID == sc.SpanID() {
			flags |= flagIsRemote
		}
	}
	b = appendText(b, 5, s.Name)
	b = appendVarint(b, 6, uint64(s.Kind)) //nolint:gosec // a kind is a small positive number, numbered as in OTLP
	b = appendFixed64(b, 7, nanos(s.Start))
	b = appendFixed64(b, 8, nanos(s.End))
	for _, kv := range s.Attrs {
		b = appendKeyValue(b, 9, kv)
	}
	var status int
	b, status = openField(b, 15)
	switch {
	case s.Failed:
		b = appendVarint(b, 3, 2)
	case s.OK:
		b = appendVarint(b, 3, 1)
	}
	b = closeField(b, status)
	return appendFixed32(b, 16, flags)
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
