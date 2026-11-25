package export

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"encoding/json/jsontext"
	"encoding/json/v2"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// lineWriter writes one call line. The tokens are always well formed and a
// buffer takes every write, so no write can fail and none is checked.
type lineWriter struct{ e *jsontext.Encoder }

func (w lineWriter) put(tokens ...jsontext.Token) {
	for _, t := range tokens {
		_ = w.e.WriteToken(t)
	}
}

// appendLine writes one call as a line of flat JSON, with OpenTelemetry's
// names for its fields: the record's time, level, trace and attributes,
// and the call's spans inside the attributes as hotline.trace.spans
// (Peter, 2026-11-11). A body is a byte slice, written as base64, which
// needs no escaping. Under content: full, what the record holds in full
// follows the record's own attributes, under hotline.content.* names, so
// the record's hash still rebuilds from the line (Peter, 2026-11-16).
func appendLine(buf *bytes.Buffer, resource []attribute.KeyValue, c Call, withContent bool) {
	w := lineWriter{jsontext.NewEncoder(buf, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))}
	w.put(
		jsontext.BeginObject,
		jsontext.String("time"), jsontext.String(timeText(c.At)),
		jsontext.String("observed_time"), jsontext.String(timeText(c.Observed)),
		jsontext.String("severity"), jsontext.String(c.Severity.String()),
		jsontext.String("event"), jsontext.String(c.event()),
		jsontext.String("trace_id"), jsontext.String(c.Trace.TraceID().String()),
		jsontext.String("span_id"), jsontext.String(c.Trace.SpanID().String()),
		jsontext.String("flags"), jsontext.String(flagsText(recordFlags(c.Trace))),
	)
	if c.Body != "" {
		w.put(jsontext.String("body"), jsontext.String(c.Body))
	}
	w.put(jsontext.String("resource"))
	w.object(resource)
	w.put(jsontext.String("attributes"), jsontext.BeginObject)
	w.entries(c.Attrs)
	if withContent {
		w.entries(c.Content)
	}
	w.put(jsontext.String(TraceSpans), jsontext.BeginArray)
	for _, s := range c.Spans {
		w.span(c.Trace, s)
	}
	// The encoder ends the value with a line break.
	w.put(jsontext.EndArray, jsontext.EndObject, jsontext.EndObject)
}

// appendBatchLine writes a ledger batch or drop record as a line of flat
// JSON: its times, level, event, resource and attributes, every field its
// signature covers.
func appendBatchLine(buf *bytes.Buffer, resource []attribute.KeyValue, b Batch) {
	w := lineWriter{jsontext.NewEncoder(buf, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))}
	w.put(
		jsontext.BeginObject,
		jsontext.String("time"), jsontext.String(timeText(b.At)),
		jsontext.String("observed_time"), jsontext.String(timeText(b.Observed)),
		jsontext.String("severity"), jsontext.String(b.Severity.String()),
		jsontext.String("event"), jsontext.String(b.Event),
		jsontext.String("resource"),
	)
	w.object(resource)
	w.put(jsontext.String("attributes"))
	w.object(b.Attrs)
	w.put(jsontext.EndObject)
}

// flagsText is a record's trace flags as two hex digits, as the trace
// header writes them.
func flagsText(flags uint32) string { return fmt.Sprintf("%02x", flags) }

// span writes one step of the call, in the trace sc, which is the line's
// own.
func (w lineWriter) span(sc trace.SpanContext, s Span) {
	parent := jsontext.Null
	if s.Parent.IsValid() {
		parent = jsontext.String(s.Parent.String())
	}
	w.put(
		jsontext.BeginObject,
		jsontext.String("span_id"), jsontext.String(s.ID.String()),
		jsontext.String("parent_span_id"), parent,
		jsontext.String("name"), jsontext.String(s.Name),
		jsontext.String("kind"), jsontext.String(s.Kind.String()),
		jsontext.String("start_time"), jsontext.String(timeText(s.Start)),
		jsontext.String("end_time"), jsontext.String(timeText(s.End)),
		jsontext.String("status"), jsontext.String(s.Status.String()),
		jsontext.String("flags"), jsontext.Uint(uint64(s.flags(sc))),
		jsontext.String("attributes"),
	)
	w.object(s.Attrs)
	if len(s.Links) > 0 {
		w.put(jsontext.String("links"), jsontext.BeginArray)
		for _, link := range s.Links {
			w.put(jsontext.BeginObject,
				jsontext.String("trace_id"), jsontext.String(link.TraceID().String()),
				jsontext.String("span_id"), jsontext.String(link.SpanID().String()),
				jsontext.String("flags"), jsontext.Uint(uint64(linkFlags(link))),
				jsontext.EndObject)
		}
		w.put(jsontext.EndArray)
	}
	w.put(jsontext.EndObject)
}

// object writes attributes as one JSON object.
func (w lineWriter) object(attrs []attribute.KeyValue) {
	w.put(jsontext.BeginObject)
	w.entries(attrs)
	w.put(jsontext.EndObject)
}

// entries writes each attribute as a name and its value, inside an object
// already begun.
func (w lineWriter) entries(attrs []attribute.KeyValue) {
	for _, kv := range attrs {
		w.put(jsontext.String(string(kv.Key)))
		w.value(kv.Value)
	}
}

// value writes one value: numbers as numbers, lists as arrays, maps as
// objects and bytes as base64 text, the way Go's JSON writes them.
func (w lineWriter) value(v attribute.Value) {
	switch v.Type() {
	case attribute.SLICE:
		w.put(jsontext.BeginArray)
		for _, item := range v.AsSlice() {
			w.value(item)
		}
		w.put(jsontext.EndArray)
	case attribute.MAP:
		w.object(v.AsMap())
	case attribute.FLOAT64:
		// A decimal number always has a point or an exponent, so a reader
		// can tell it from a whole number: the ledger's hashes tell them
		// apart (Peter, 2026-11-16).
		_ = w.e.WriteValue(jsontext.Value(decimal(v.AsFloat64())))
	default:
		_ = json.MarshalEncode(w.e, v.AsInterface())
	}
}

// timeText is a time in UTC, to the microsecond, as RFC 3339 writes it.
func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

// decimal writes a decimal number as JSON, with a point or an exponent.
// A record's decimals come from JSON bodies, so they are never infinite.
func decimal(f float64) string {
	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".e") {
		text += ".0"
	}
	return text
}
