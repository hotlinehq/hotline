package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
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
	// StreamEvent is the event of a streamed response's line, written when the
	// stream ends, and StreamItemEvent that of one stream item of it, such as one
	// Server-Sent Event (Peter, 2026-11-21, M2, Q1 and Q3).
	StreamEvent     = "hotline.stream"
	StreamItemEvent = "hotline.stream.item"
	// ProbeEvent is the event of a probe's line: a check of a vendor
	// endpoint after calls to it failed to connect (Peter, 2026-11-22).
	ProbeEvent = "hotline.probe"
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

// Line is one line of a file exporter's file, read back: a call, or a
// ledger batch record. Its attribute values are as JSON gives them,
// numbers kept as written. A field the line does not hold is empty.
type Line struct {
	Time     time.Time
	Observed time.Time
	Severity string
	Event    string
	TraceID  string
	SpanID   string
	// Flags are the record's trace flags, from two hex digits.
	Flags      uint32
	Body       string
	Resource   map[string]any
	Attributes map[string]any
	// Spans are a call's spans, from its hotline.trace.spans; none for a
	// batch.
	Spans []LineSpan
}

// LineSpan is one step of a call, as its line holds it.
type LineSpan struct {
	ID, Parent, Name, Kind, Status string
	Start, End                     time.Time
	Flags                          uint32
	Links                          []LineLink
	Attributes                     map[string]any
}

// LineLink is a span of another trace that a span follows from.
type LineLink struct {
	TraceID, SpanID string
	Flags           uint32
}

// rawLine is a line as appendLine and appendBatchLine write it.
type rawLine struct {
	Time       string         `json:"time"`
	Observed   string         `json:"observed_time"`
	Severity   string         `json:"severity"`
	Event      string         `json:"event"`
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Flags      string         `json:"flags"`
	Body       string         `json:"body"`
	Resource   map[string]any `json:"resource"`
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
	line := Line{
		Time: when, Severity: raw.Severity, Event: raw.Event, TraceID: raw.TraceID, SpanID: raw.SpanID,
		Body: raw.Body, Resource: raw.Resource, Attributes: raw.Attributes,
	}
	// A field that cannot be read is read as nothing, which the record's
	// hash does not match. A batch record has no flags.
	flags, _ := strconv.ParseUint(raw.Flags, 16, 8)
	line.Flags = uint32(flags)
	line.Observed, _ = time.Parse(time.RFC3339Nano, raw.Observed)
	spans, _ := raw.Attributes[TraceSpans].([]any)
	for _, item := range spans {
		s, _ := item.(map[string]any)
		line.Spans = append(line.Spans, readSpan(s))
	}
	return line, nil
}

// readSpan reads one span of a line's hotline.trace.spans. A field that
// cannot be read is read as nothing, which the trace's hash does not match.
func readSpan(s map[string]any) LineSpan {
	text := func(m map[string]any, name string) string {
		t, _ := m[name].(string)
		return t
	}
	at := func(name string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, text(s, name))
		return t
	}
	flags := func(m map[string]any) uint32 {
		n, _ := m["flags"].(json.Number)
		f, _ := strconv.ParseUint(n.String(), 10, 32)
		return uint32(f)
	}
	attrs, _ := s["attributes"].(map[string]any)
	span := LineSpan{
		ID: text(s, "span_id"), Parent: text(s, "parent_span_id"), Name: text(s, "name"), Kind: text(s, "kind"),
		Start: at("start_time"), End: at("end_time"), Status: text(s, "status"), Flags: flags(s), Attributes: attrs,
	}
	links, _ := s["links"].([]any)
	for _, item := range links {
		link, _ := item.(map[string]any)
		span.Links = append(span.Links, LineLink{TraceID: text(link, "trace_id"), SpanID: text(link, "span_id"), Flags: flags(link)})
	}
	return span
}
