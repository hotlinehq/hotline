package export

import (
	"bytes"
	"encoding/base64"
	"time"

	"encoding/json/jsontext"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// appendLine writes one call as a line of flat JSON, with OpenTelemetry's
// names for its fields: the record's time, level, trace and attributes,
// and the call's spans inside the attributes as hotline.trace.spans
// (Peter, 2026-11-11). Text is quoted the JSON way; a body is a byte slice
// and so base64, which needs no escaping.
func appendLine(buf *bytes.Buffer, resource []attribute.KeyValue, c Call, attrs []attribute.KeyValue) error {
	e := jsontext.NewEncoder(buf, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	tokens := []jsontext.Token{
		jsontext.BeginObject,
		jsontext.String("time"), jsontext.String(timeText(c.At)),
		jsontext.String("severity"), jsontext.String(severityText(level(attrs))),
		jsontext.String("event"), jsontext.String("hotline.request"),
	}
	if c.Trace.HasTraceID() {
		tokens = append(tokens, jsontext.String("trace_id"), jsontext.String(c.Trace.TraceID().String()))
	}
	if c.Trace.HasSpanID() {
		tokens = append(tokens, jsontext.String("span_id"), jsontext.String(c.Trace.SpanID().String()))
	}
	tokens = append(tokens, jsontext.String("resource"))
	if err := writeTokens(e, tokens...); err != nil {
		return err
	}
	if err := writeMap(e, resource); err != nil {
		return err
	}
	if err := writeTokens(e, jsontext.String("attributes"), jsontext.BeginObject); err != nil {
		return err
	}
	if err := writeEntries(e, attrs); err != nil {
		return err
	}
	if err := writeTokens(e, jsontext.String("hotline.trace.spans"), jsontext.BeginArray); err != nil {
		return err
	}
	for _, s := range c.Spans {
		if err := writeSpan(e, s); err != nil {
			return err
		}
	}
	// The encoder ends the value with a line break.
	return writeTokens(e, jsontext.EndArray, jsontext.EndObject, jsontext.EndObject)
}

// writeSpan writes one step of the call. The trace ID is the line's own.
func writeSpan(e *jsontext.Encoder, s Span) error {
	parent := jsontext.Null
	if s.Parent.IsValid() {
		parent = jsontext.String(s.Parent.String())
	}
	status := "unset"
	switch {
	case s.Failed:
		status = "error"
	case s.OK:
		status = "ok"
	}
	err := writeTokens(e,
		jsontext.BeginObject,
		jsontext.String("span_id"), jsontext.String(s.ID.String()),
		jsontext.String("parent_span_id"), parent,
		jsontext.String("name"), jsontext.String(s.Name),
		jsontext.String("kind"), jsontext.String(kindText(s.Kind)),
		jsontext.String("start_time"), jsontext.String(timeText(s.Start)),
		jsontext.String("end_time"), jsontext.String(timeText(s.End)),
		jsontext.String("status"), jsontext.String(status),
	)
	if err != nil {
		return err
	}
	if len(s.Attrs) > 0 {
		if err := e.WriteToken(jsontext.String("attributes")); err != nil {
			return err
		}
		if err := writeMap(e, s.Attrs); err != nil {
			return err
		}
	}
	return e.WriteToken(jsontext.EndObject)
}

// writeMap writes attributes as one JSON object.
func writeMap(e *jsontext.Encoder, attrs []attribute.KeyValue) error {
	if err := e.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	if err := writeEntries(e, attrs); err != nil {
		return err
	}
	return e.WriteToken(jsontext.EndObject)
}

// writeEntries writes each attribute as a name and its value, inside an
// object already begun.
func writeEntries(e *jsontext.Encoder, attrs []attribute.KeyValue) error {
	for _, kv := range attrs {
		if err := e.WriteToken(jsontext.String(string(kv.Key))); err != nil {
			return err
		}
		if err := writeValue(e, kv.Value); err != nil {
			return err
		}
	}
	return nil
}

// writeValue writes one value: numbers as numbers, lists as arrays, maps
// as objects and bytes as base64 text.
func writeValue(e *jsontext.Encoder, v attribute.Value) error {
	switch v.Type() {
	case attribute.BOOL:
		return e.WriteToken(jsontext.Bool(v.AsBool()))
	case attribute.INT64:
		return e.WriteToken(jsontext.Int(v.AsInt64()))
	case attribute.FLOAT64:
		return e.WriteToken(jsontext.Float(v.AsFloat64()))
	case attribute.STRING:
		return e.WriteToken(jsontext.String(v.AsString()))
	case attribute.BYTESLICE:
		return e.WriteToken(jsontext.String(base64.StdEncoding.EncodeToString(v.AsByteSlice())))
	case attribute.STRINGSLICE:
		return writeList(e, v.AsStringSlice(), attribute.StringValue)
	case attribute.BOOLSLICE:
		return writeList(e, v.AsBoolSlice(), attribute.BoolValue)
	case attribute.INT64SLICE:
		return writeList(e, v.AsInt64Slice(), attribute.Int64Value)
	case attribute.FLOAT64SLICE:
		return writeList(e, v.AsFloat64Slice(), attribute.Float64Value)
	case attribute.SLICE:
		return writeList(e, v.AsSlice(), func(item attribute.Value) attribute.Value { return item })
	case attribute.MAP:
		return writeMap(e, v.AsMap())
	}
	return e.WriteToken(jsontext.Null)
}

func writeList[T any](e *jsontext.Encoder, items []T, value func(T) attribute.Value) error {
	if err := e.WriteToken(jsontext.BeginArray); err != nil {
		return err
	}
	for _, item := range items {
		if err := writeValue(e, value(item)); err != nil {
			return err
		}
	}
	return e.WriteToken(jsontext.EndArray)
}

func writeTokens(e *jsontext.Encoder, tokens ...jsontext.Token) error {
	for _, t := range tokens {
		if err := e.WriteToken(t); err != nil {
			return err
		}
	}
	return nil
}

// timeText is a time in UTC, to the microsecond, as RFC 3339 writes it.
func timeText(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

// severityText is the level's name, as OpenTelemetry writes it.
func severityText(s log.Severity) string {
	switch {
	case s >= log.SeverityError:
		return "ERROR"
	case s >= log.SeverityWarn:
		return "WARN"
	}
	return "INFO"
}

// kindText is a span's kind in lower case, as OpenTelemetry names it.
func kindText(k trace.SpanKind) string {
	switch k {
	case trace.SpanKindServer:
		return "server"
	case trace.SpanKindClient:
		return "client"
	case trace.SpanKindProducer:
		return "producer"
	case trace.SpanKindConsumer:
		return "consumer"
	}
	return "internal"
}
