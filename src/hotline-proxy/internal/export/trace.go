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

// Trace queues the spans of one call, in the trace sc names, for every
// destination. It never blocks: a full queue drops its oldest span. The
// spans are written once, into one block that every destination shares.
func (s *Sink) Trace(sc trace.SpanContext, spans []Span) {
	scratch := s.scratch.Get().(*[]byte)
	b := (*scratch)[:0]
	ends := make([]int, len(spans))
	for i, span := range spans {
		b = appendSpan(b, sc, span)
		ends[i] = len(b)
	}
	written := slices.Clone(b)
	*scratch = b
	s.scratch.Put(scratch)
	for _, d := range s.destinations {
		start := 0
		for _, end := range ends {
			d.spans.add(written[start:end:end])
			start = end
		}
	}
}
