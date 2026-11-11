package export

import (
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Span is one step of a call, with its own start and end.
type Span struct {
	ID trace.SpanID
	// Parent is the span this one sits under; none for a span at the top
	// of the proxy's part of the trace.
	Parent trace.SpanID
	Name   string
	Kind   trace.SpanKind
	Start  time.Time
	End    time.Time
	Failed bool
	// OK marks a step that proved something went right, such as a check
	// that passed (Peter, 2026-10-31). Other spans leave it unset.
	OK    bool
	Attrs []attribute.KeyValue
}

// trace queues the spans of one call, in the trace sc names, for every
// OTLP destination. It never blocks: a queue without room for all of the call's
// spans refuses them all, so a trace is never split. The spans are written
// once, into one block that every destination shares.
func (s *Sink) trace(sc trace.SpanContext, spans []Span) {
	scratch := s.scratch.Get().(*[]byte)
	b := (*scratch)[:0]
	items := make([][]byte, len(spans))
	// A call's span ends fit on the stack, so they cost no allocation.
	var endsOnStack [16]int
	ends := endsOnStack[:0]
	for _, span := range spans {
		b = appendSpan(b, sc, span)
		ends = append(ends, len(b))
	}
	written := slices.Clone(b)
	*scratch = b
	s.scratch.Put(scratch)
	start := 0
	for i, end := range ends {
		items[i] = written[start:end:end]
		start = end
	}
	for _, d := range s.destinations {
		d.spans.add(items...)
	}
}
