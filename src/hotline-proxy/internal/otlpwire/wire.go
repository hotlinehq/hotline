// Package otlpwire writes the proxy's call records and spans straight into
// OTLP protobuf bytes, without the OpenTelemetry log and trace libraries.
//
// SPIKE: throwaway code to measure the gain (docs/otlp-encoder-scope.md).
// It is not wired into the proxy.
package otlpwire

import (
	"math"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protowire"
)

// open starts a length-prefixed field with a one-byte length placeholder and
// returns where its content starts.
func open(b []byte, field protowire.Number) ([]byte, int) {
	b = protowire.AppendTag(b, field, protowire.BytesType)
	b = append(b, 0)
	return b, len(b)
}

// close writes the length of the field opened at start, moving its content
// when the length needs more than one byte.
func close(b []byte, start int) []byte {
	n := len(b) - start
	if n < 0x80 {
		b[start-1] = byte(n)
		return b
	}
	size := protowire.SizeVarint(uint64(n))
	b = append(b, make([]byte, size-1)...)
	copy(b[start+size-1:], b[start:start+n])
	protowire.AppendVarint(b[start-1:start-1], uint64(n))
	return b
}

func appendString(b []byte, field protowire.Number, s string) []byte {
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

func nanos(t time.Time) uint64 {
	if n := t.UnixNano(); n > 0 {
		return uint64(n)
	}
	return 0
}

// appendKeyValue writes one KeyValue message as field.
func appendKeyValue(b []byte, field protowire.Number, kv attribute.KeyValue) []byte {
	b, start := open(b, field)
	b = appendString(b, 1, string(kv.Key))
	b = appendValue(b, 2, kv.Value)
	return close(b, start)
}

// appendValue writes one AnyValue message as field.
func appendValue(b []byte, field protowire.Number, v attribute.Value) []byte {
	b, start := open(b, field)
	switch v.Type() {
	case attribute.BOOL:
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, protowire.EncodeBool(v.AsBool()))
	case attribute.INT64:
		b = protowire.AppendTag(b, 3, protowire.VarintType)
		b = protowire.AppendVarint(b, uint64(v.AsInt64()))
	case attribute.FLOAT64:
		b = protowire.AppendTag(b, 4, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, math.Float64bits(v.AsFloat64()))
	case attribute.STRING:
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendString(b, v.AsString())
	case attribute.BYTESLICE:
		b = appendBytes(b, 7, v.AsByteSlice())
	case attribute.STRINGSLICE:
		var arr int
		b, arr = open(b, 5)
		for _, s := range v.AsStringSlice() {
			b = appendValue(b, 1, attribute.StringValue(s))
		}
		b = close(b, arr)
	case attribute.INT64SLICE:
		var arr int
		b, arr = open(b, 5)
		for _, n := range v.AsInt64Slice() {
			b = appendValue(b, 1, attribute.Int64Value(n))
		}
		b = close(b, arr)
	case attribute.FLOAT64SLICE:
		var arr int
		b, arr = open(b, 5)
		for _, f := range v.AsFloat64Slice() {
			b = appendValue(b, 1, attribute.Float64Value(f))
		}
		b = close(b, arr)
	case attribute.BOOLSLICE:
		var arr int
		b, arr = open(b, 5)
		for _, t := range v.AsBoolSlice() {
			b = appendValue(b, 1, attribute.BoolValue(t))
		}
		b = close(b, arr)
	case attribute.SLICE:
		var arr int
		b, arr = open(b, 5)
		for _, item := range v.AsSlice() {
			b = appendValue(b, 1, item)
		}
		b = close(b, arr)
	case attribute.MAP:
		var list int
		b, list = open(b, 6)
		for _, kv := range v.AsMap() {
			b = appendKeyValue(b, 1, kv)
		}
		b = close(b, list)
	}
	return close(b, start)
}

// Record is one hotline.request log record.
type Record struct {
	At           time.Time
	Observed     time.Time
	Severity     int
	SeverityText string
	EventName    string
	Context      trace.SpanContext
	Attrs        []attribute.KeyValue
}

// AppendRecord writes the content of one LogRecord message, without its
// tag and length, so it can be placed in any batch.
func AppendRecord(b []byte, r Record) []byte {
	b = appendFixed64(b, 1, nanos(r.At))
	b = appendVarint(b, 2, uint64(r.Severity))
	b = appendString(b, 3, r.SeverityText)
	// The library always sends an empty body; kept so both match.
	b = append(protowire.AppendTag(b, 5, protowire.BytesType), 0)
	for _, kv := range r.Attrs {
		b = appendKeyValue(b, 6, kv)
	}
	b = appendFixed32(b, 8, uint32(r.Context.TraceFlags()))
	if id := r.Context.TraceID(); id.IsValid() {
		b = appendBytes(b, 9, id[:])
	}
	if id := r.Context.SpanID(); id.IsValid() {
		b = appendBytes(b, 10, id[:])
	}
	b = appendFixed64(b, 11, nanos(r.Observed))
	return appendString(b, 12, r.EventName)
}

// Span is one step of a call's trace.
type Span struct {
	Trace  trace.SpanContext
	ID     trace.SpanID
	Parent trace.SpanID
	Name   string
	Kind   trace.SpanKind
	Start  time.Time
	End    time.Time
	// Status is 0 unset, 1 ok, 2 error, as in OTLP.
	Status int
	Attrs  []attribute.KeyValue
}

// Flags as the OTLP trace exporter sets them: the W3C flags, and that the
// parent's remoteness is known. The proxy marks every parent remote.
const (
	hasIsRemote = 0x100
	isRemote    = 0x200
)

// AppendSpan writes the content of one Span message, without its tag and
// length.
func AppendSpan(b []byte, s Span) []byte {
	traceID := s.Trace.TraceID()
	b = appendBytes(b, 1, traceID[:])
	b = appendBytes(b, 2, s.ID[:])
	flags := uint32(s.Trace.TraceFlags()) | hasIsRemote
	if s.Parent.IsValid() {
		b = appendBytes(b, 4, s.Parent[:])
		flags |= isRemote
	}
	b = appendString(b, 5, s.Name)
	b = appendVarint(b, 6, uint64(s.Kind))
	b = appendFixed64(b, 7, nanos(s.Start))
	b = appendFixed64(b, 8, nanos(s.End))
	for _, kv := range s.Attrs {
		b = appendKeyValue(b, 9, kv)
	}
	b, start := open(b, 15)
	b = appendVarint(b, 3, uint64(s.Status))
	b = close(b, start)
	return appendFixed32(b, 16, flags)
}

// Resource writes the content of a Resource message.
func Resource(attrs []attribute.KeyValue) []byte {
	var b []byte
	for _, kv := range attrs {
		b = appendKeyValue(b, 1, kv)
	}
	return b
}

// Batch writes a whole export request, ExportLogsServiceRequest or
// ExportTraceServiceRequest (they share field numbers), from the items' own
// encoded content: one resource, one scope, and the items one after another.
func Batch(b []byte, resource []byte, scope string, items [][]byte) []byte {
	b, outer := open(b, 1) // resource_logs or resource_spans
	b = appendBytes(b, 1, resource)
	b, inner := open(b, 2) // scope_logs or scope_spans
	b, sc := open(b, 1)
	b = appendString(b, 1, scope)
	b = close(b, sc)
	for _, item := range items {
		b = appendBytes(b, 2, item)
	}
	b = close(b, inner)
	return close(b, outer)
}
