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
	at := time.Date(2026, 11, 16, 12, 0, 0, 123456000, time.UTC)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	link := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{9}, SpanID: trace.SpanID{8}, TraceFlags: trace.FlagsSampled})
	call := export.Call{
		Trace: sc, At: at, Observed: at.Add(time.Second), Severity: log.SeverityInfo, Body: "a note",
		Attrs: []attribute.KeyValue{
			attribute.String("hotline.integration.id", "checkout"),
			attribute.String("hotline.content.hash", "sha256:00"),
			attribute.String("hotline.record.hash", "sha256:11"),
		},
		Content: []attribute.KeyValue{attribute.String("hotline.content.request.body", "e30=")},
		Spans: []export.Span{{
			ID: trace.SpanID{7}, Parent: sc.SpanID(), Name: "vendor call", Kind: trace.SpanKindClient,
			Start: at, End: at.Add(time.Millisecond), Status: export.StatusError, Links: []trace.SpanContext{link},
		}},
	}

	It("gives back the time, event, trace, attributes and spans the writer wrote", func() {
		line, err := export.ReadLine(export.LineOf(call))
		Expect(err).NotTo(HaveOccurred())
		Expect(line.Time).To(BeTemporally("==", at))
		Expect(line.Observed).To(BeTemporally("==", at.Add(time.Second)))
		Expect(line.Flags).To(Equal(uint32(trace.FlagsSampled)))
		Expect(line.Severity).To(Equal("INFO"))
		Expect(line.Body).To(Equal("a note"))
		Expect(line.Resource).To(HaveKeyWithValue("service.name", "hotline-proxy"))
		Expect(line.Event).To(Equal(export.CallEvent))
		Expect(line.TraceID).To(Equal(sc.TraceID().String()))
		Expect(line.SpanID).To(Equal(sc.SpanID().String()))
		Expect(line.Attributes).To(HaveKeyWithValue("hotline.integration.id", "checkout"))
		Expect(line.Spans).To(HaveLen(1))
		span := line.Spans[0]
		Expect([]string{span.ID, span.Parent, span.Name, span.Kind, span.Status}).To(Equal([]string{
			trace.SpanID{7}.String(), sc.SpanID().String(), "vendor call", "client", "error",
		}))
		Expect(span.Start).To(BeTemporally("==", at))
		Expect(span.End).To(BeTemporally("==", at.Add(time.Millisecond)))
		// The flags say, as OTLP's do, that the span's parent is in this
		// program, not another one.
		Expect(span.Flags).To(Equal(uint32(0x101)))
		Expect(span.Links).To(Equal([]export.LineLink{{TraceID: link.TraceID().String(), SpanID: link.SpanID().String(), Flags: 0x101}}))
	})

	It("adds nothing the record's hash covers beyond the record's own attributes", func() {
		line, err := export.ReadLine(export.LineOf(call))
		Expect(err).NotTo(HaveOccurred())
		own := map[string]bool{}
		for _, kv := range call.Attrs {
			own[string(kv.Key)] = true
		}
		for name := range line.Attributes {
			if !own[name] {
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
