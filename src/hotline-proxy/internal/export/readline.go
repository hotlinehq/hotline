package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

// The names a file line is written with. hotline-proxy verify reads lines
// back with ReadLine, so the writer and the reader share them.
const (
	// CallEvent is the event of a call's line.
	CallEvent = "hotline.request"
	// SessionEvent is the event of a WebSocket session's line, which is
	// written like a call's (Peter, 2026-11-18).
	SessionEvent = "hotline.websocket.session"
	// MessageEvent is the event of one message of a WebSocket session
	// (Peter, 2026-11-19, WebSocket slice 2, Q1).
	MessageEvent = "hotline.websocket.message"
	// BatchEvent is the event of a ledger batch's line.
	BatchEvent = "hotline.ledger.batch"
	// TraceSpans is the attribute of a call's line that holds its spans.
	TraceSpans = "hotline.trace.spans"
)

var (
	// ErrNotALine says a line is not one the file exporter writes.
	ErrNotALine = errors.New("not a line the file exporter writes")
	// ErrNoTime says a line has no time that can be read.
	ErrNoTime = errors.New("the line has no time")
)

// Line is one line of a file exporter's file, read back: a call or a
// ledger batch. Its attribute values are as JSON gives them, numbers kept
// as written.
type Line struct {
	Time       time.Time
	Event      string
	TraceID    string
	SpanID     string
	Attributes map[string]any
	// Spans are a call's spans, from its hotline.trace.spans; none for a
	// batch.
	Spans []LineSpan
}

// LineSpan is one step of a call, as its line holds it.
type LineSpan struct {
	ID, Parent, Name, Kind, Status string
	Start, End                     time.Time
	Attributes                     map[string]any
}

// rawLine is a line as appendLine and appendBatchLine write it.
type rawLine struct {
	Time       string         `json:"time"`
	Event      string         `json:"event"`
	TraceID    string         `json:"trace_id"` //nolint:tagliatelle // hotline.verify.v1 writes names in snake case, as agreed
	SpanID     string         `json:"span_id"`  //nolint:tagliatelle // hotline.verify.v1 writes names in snake case, as agreed
	Attributes map[string]any `json:"attributes"`
}

// ReadLine reads one line back as the file exporter wrote it.
func ReadLine(data []byte) (Line, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw rawLine
	if err := decoder.Decode(&raw); err != nil || raw.Attributes == nil {
		return Line{}, ErrNotALine
	}
	when, err := time.Parse(time.RFC3339Nano, raw.Time)
	if err != nil {
		return Line{}, ErrNoTime
	}
	line := Line{Time: when, Event: raw.Event, TraceID: raw.TraceID, SpanID: raw.SpanID, Attributes: raw.Attributes}
	spans, _ := raw.Attributes[TraceSpans].([]any)
	for _, item := range spans {
		s, _ := item.(map[string]any)
		text := func(name string) string {
			t, _ := s[name].(string)
			return t
		}
		at := func(name string) time.Time {
			t, _ := time.Parse(time.RFC3339Nano, text(name))
			return t
		}
		attrs, _ := s["attributes"].(map[string]any)
		line.Spans = append(line.Spans, LineSpan{
			ID: text("span_id"), Parent: text("parent_span_id"), Name: text("name"), Kind: text("kind"),
			Start: at("start_time"), End: at("end_time"), Status: text("status"), Attributes: attrs,
		})
	}
	return line, nil
}
