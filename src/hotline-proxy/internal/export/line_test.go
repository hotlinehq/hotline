package export_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
)

// A call's line is read back by hotline-proxy verify, which rebuilds the
// record's hash from it, so the reader and the rule for what the hash
// covers must match what the writer puts in the line.
var _ = Describe("Reading back a call's line", func() {
	sut := lineSUT{}

	It("gives back the time, event, trace, attributes and spans the writer wrote", func() {
		sut.forCallWrittenToAFile()
		line, err := export.ReadLine(sut.WrittenLine())
		Expect(err).NotTo(HaveOccurred())
		Expect(line.Time).To(BeTemporally("==", sut.CallTime()))
		Expect(line.Observed).To(BeTemporally("==", sut.CallTime().Add(time.Second)))
		Expect(line.Flags).To(Equal(uint32(trace.FlagsSampled)))
		Expect(line.Severity).To(Equal("INFO"))
		Expect(line.Body).To(Equal("a note"))
		Expect(line.Resource).To(HaveKeyWithValue("service.name", "hotline-proxy"))
		Expect(line.Event).To(Equal("hotline.request"))
		Expect(line.TraceID).To(Equal("01020300000000000000000000000000"))
		Expect(line.SpanID).To(Equal("0405060000000000"))
		Expect(line.Attributes).To(HaveKeyWithValue("hotline.integration.id", "checkout"))
		Expect(line.Spans).To(HaveLen(1))
		span := line.Spans[0]
		Expect([]string{span.ID, span.Parent, span.Name, span.Kind, span.Status}).To(Equal([]string{
			"0700000000000000", "0405060000000000", "vendor call", "client", "error",
		}))
		Expect(span.Start).To(BeTemporally("==", sut.CallTime()))
		Expect(span.End).To(BeTemporally("==", sut.CallTime().Add(time.Millisecond)))
		// The flags say, as OTLP's do, that the span's parent is in this
		// program, not another one.
		Expect(span.Flags).To(Equal(uint32(0x101)))
		Expect(span.Links).To(Equal([]export.LineLink{{TraceID: "09000000000000000000000000000000", SpanID: "0800000000000000", Flags: 0x101}}))
	})

	It("adds nothing the record's hash covers beyond the record's own attributes", func() {
		sut.forCallWrittenToAFile()
		line, err := export.ReadLine(sut.WrittenLine())
		Expect(err).NotTo(HaveOccurred())
		for name := range line.Attributes {
			if !sut.OwnAttribute(name) {
				Expect(ledger.RecordCovers(name)).To(BeFalse(), "the line adds %s, which the record's hash would cover", name)
			}
		}
	})

	It("refuses a line that is not JSON", func() {
		_, err := export.ReadLine([]byte("not json"))
		Expect(err).To(MatchError(export.ErrNotALine))
	})

	It("refuses a line with no time", func() {
		_, err := export.ReadLine([]byte(`{"event":"hotline.request","attributes":{}}`))
		Expect(err).To(MatchError(export.ErrNoTime))
	})
})

// lineSUT is one finished call, with a span and a link, written by a file
// exporter.
type lineSUT struct {
	at       time.Time
	call     export.Call
	exporter exportersSUT
}

// forCallWrittenToAFile is a call with a note, three attributes, a body
// kept in full and one vendor call's span, written by a file exporter.
func (s *lineSUT) forCallWrittenToAFile() {
	s.at = time.Date(2026, 11, 16, 12, 0, 0, 123456000, time.UTC)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	link := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{9}, SpanID: trace.SpanID{8}, TraceFlags: trace.FlagsSampled})
	s.call = export.Call{
		Trace: sc, At: s.at, Observed: s.at.Add(time.Second), Severity: log.SeverityInfo, Body: "a note",
		Attrs: []attribute.KeyValue{
			attribute.String("hotline.integration.id", "checkout"),
			attribute.String("hotline.content.hash", "sha256:00"),
			attribute.String("hotline.record.hash", "sha256:11"),
		},
		Content: []attribute.KeyValue{attribute.String("hotline.content.request.body", "e30=")},
		Spans: []export.Span{{
			ID: trace.SpanID{7}, Parent: sc.SpanID(), Name: "vendor call", Kind: trace.SpanKindClient,
			Start: s.at, End: s.at.Add(time.Millisecond), Status: export.StatusError, Links: []trace.SpanContext{link},
		}},
	}
	s.exporter.forFileExporter()
	s.exporter.Call(s.call)
	s.exporter.Stop()
}

// WrittenLine is the one line the file exporter wrote, without its line
// break.
func (s *lineSUT) WrittenLine() []byte {
	text := s.exporter.FileText()
	Expect(text).WithOffset(1).To(HaveSuffix("\n"))
	return []byte(text[:len(text)-1])
}

// CallTime is when the call happened.
func (s *lineSUT) CallTime() time.Time { return s.at }

// OwnAttribute says the call itself carries the attribute name.
func (s *lineSUT) OwnAttribute(name string) bool {
	for _, kv := range s.call.Attrs {
		if string(kv.Key) == name {
			return true
		}
	}
	return false
}
