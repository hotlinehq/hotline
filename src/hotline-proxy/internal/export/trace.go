package export

import (
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/ledger"
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
	Status Status
	Attrs  []attribute.KeyValue
	// Links are spans of other traces this one follows from, such as the
	// failed call that made a probe due.
	Links []trace.SpanContext
}

// Status is how a step ended, with OpenTelemetry's three span statuses.
type Status int

const (
	// StatusUnset is a step that says nothing either way, most of them.
	StatusUnset Status = iota
	// StatusOK is a step that proved something went right, such as a
	// check that passed (Peter, 2026-10-31).
	StatusOK
	// StatusError is a step that failed.
	StatusError
)

// String is the status as a file line and a trace's hash write it.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	}
	return "unset"
}

// FailedIf is StatusError when failed, and StatusUnset otherwise.
func FailedIf(failed bool) Status {
	if failed {
		return StatusError
	}
	return StatusUnset
}

// The span flags of OTLP besides the W3C trace flags: that the span says
// whether its parent is remote, and that it is.
const (
	flagHasIsRemote = 0x100
	flagIsRemote    = 0x200
)

// flags are the span's flags as OTLP sends them, in the trace sc: the
// trace's flags, and whether its parent is in another program. Only the
// call span's parent, the app's span, ran in another program, so only it
// is remote (Peter, 2026-11-08).
func (s Span) flags(sc trace.SpanContext) uint32 {
	flags := uint32(sc.TraceFlags()) | flagHasIsRemote
	if s.Parent.IsValid() && s.ID == sc.SpanID() {
		flags |= flagIsRemote
	}
	return flags
}

// linkFlags are a link's flags as OTLP sends them: the linked trace's
// flags, and that it says nothing on whether it is remote.
func linkFlags(link trace.SpanContext) uint32 {
	return uint32(link.TraceFlags()) | flagHasIsRemote
}

// LedgerSpans are a call's spans, in the trace sc, as the trace's hash
// covers them: every field the proxy sends, with the kind and status as
// the file exporter writes them.
func LedgerSpans(sc trace.SpanContext, spans []Span) []ledger.Span {
	out := make([]ledger.Span, len(spans))
	for i, s := range spans {
		var links []ledger.Link
		for _, link := range s.Links {
			links = append(links, ledger.Link{TraceID: link.TraceID().String(), SpanID: link.SpanID().String(), Flags: linkFlags(link)})
		}
		// An invalid parent is the zero id: no span.
		out[i] = ledger.Span{
			ID: ledger.SpanID(s.ID), Parent: ledger.SpanID(s.Parent), Name: s.Name, Kind: s.Kind.String(),
			Start: s.Start, End: s.End, Status: s.Status.String(), Flags: s.flags(sc), Links: links, Attrs: s.Attrs,
		}
	}
	return out
}

// trace queues the spans of one call, in the trace sc names, for every
// OTLP exporter. It never blocks: a queue without room for all of the call's
// spans refuses them all, so a trace is never split. The spans are written
// once, into one block that every exporter shares.
func (s *Exporters) trace(sc trace.SpanContext, spans []Span) {
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
	for _, d := range s.exporters {
		d.spans.add(items...)
	}
}
