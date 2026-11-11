package export

import (
	"bytes"
	"time"

	"encoding/json/jsontext"
	"encoding/json/v2"

	"go.opentelemetry.io/otel/attribute"
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
// needs no escaping.
func appendLine(buf *bytes.Buffer, resource []attribute.KeyValue, c Call, attrs []attribute.KeyValue) {
	w := lineWriter{jsontext.NewEncoder(buf, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))}
	w.put(
		jsontext.BeginObject,
		jsontext.String("time"), jsontext.String(timeText(c.At)),
		jsontext.String("severity"), jsontext.String(level(attrs).String()),
		jsontext.String("event"), jsontext.String("hotline.request"),
		jsontext.String("trace_id"), jsontext.String(c.Trace.TraceID().String()),
		jsontext.String("span_id"), jsontext.String(c.Trace.SpanID().String()),
		jsontext.String("resource"),
	)
	w.object(resource)
	w.put(jsontext.String("attributes"), jsontext.BeginObject)
	w.entries(attrs)
	w.put(jsontext.String("hotline.trace.spans"), jsontext.BeginArray)
	for _, s := range c.Spans {
		w.span(s)
	}
	// The encoder ends the value with a line break.
	w.put(jsontext.EndArray, jsontext.EndObject, jsontext.EndObject)
}

// span writes one step of the call. The trace ID is the line's own.
func (w lineWriter) span(s Span) {
	parent := jsontext.Null
	if s.Parent.IsValid() {
		parent = jsontext.String(s.Parent.String())
	}
	status := "unset"
	if s.OK {
		status = "ok"
	}
	if s.Failed {
		status = "error"
	}
	w.put(
		jsontext.BeginObject,
		jsontext.String("span_id"), jsontext.String(s.ID.String()),
		jsontext.String("parent_span_id"), parent,
		jsontext.String("name"), jsontext.String(s.Name),
		jsontext.String("kind"), jsontext.String(s.Kind.String()),
		jsontext.String("start_time"), jsontext.String(timeText(s.Start)),
		jsontext.String("end_time"), jsontext.String(timeText(s.End)),
		jsontext.String("status"), jsontext.String(status),
		jsontext.String("attributes"),
	)
	w.object(s.Attrs)
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
	default:
		_ = json.MarshalEncode(w.e, v.AsInterface())
	}
}

// timeText is a time in UTC, to the microsecond, as RFC 3339 writes it.
func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}
