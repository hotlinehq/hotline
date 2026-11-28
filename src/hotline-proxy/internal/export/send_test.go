package export_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/trace"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
)

var _ = Describe("sending over HTTP", func() {
	sut := sendSUT{}

	// An exporter sends from two places at once over one connection: its
	// record queue and its span queue. Sharing one gzip writer between
	// senders mixed their bodies, and could crash the proxy (fuzz run of
	// 2026-11-18).
	It("keeps the bodies of two senders apart when they send at the same time", func() {
		sut.forCollector()
		sut.ExportCallsWithASpan(200)
		Expect(sut.BrokenBodies()).To(BeZero(), "bodies the collector could not unzip")
		Expect(sut.RecordsArrived()).To(Equal(200))
		Expect(sut.SpansArrived()).To(Equal(200))
	})

	// A stop cut short by a second signal ends the send's context. A
	// collector that can't be reached looks busy, and the send used to try
	// again for as long as its own time allowed, holding the stop
	// (end-to-end run of 2026-11-24).
	It("stops trying again once its context ends", func() {
		sut.forCollectorThatIsGone()
		sut.ExportCallsWithASpan(1)
		Expect(sut.StopsAtOnceWithNoTimeLeft()).To(BeTrue())
	})
})

// sendSUT is an OTLP exporter that zips what it sends, in front of a
// fake collector that unzips and counts it.
type sendSUT struct {
	exporters exportersSUT
	collector *httptest.Server

	mu      sync.Mutex
	broken  int
	records int
	spans   int
}

// forCollector is an exporter that sends every few milliseconds to a
// collector that takes everything.
func (s *sendSUT) forCollector() {
	s.mu.Lock()
	s.broken, s.records, s.spans = 0, 0, 0
	s.mu.Unlock()
	s.collector = httptest.NewServer(http.HandlerFunc(s.receive))
	DeferCleanup(s.collector.Close)
	s.exporters.forOTLPExporter(s.exporterTo(s.collector.URL))
}

// forCollectorThatIsGone is an exporter whose each send may take a minute,
// to a collector that no longer listens, so it looks busy.
func (s *sendSUT) forCollectorThatIsGone() {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	s.exporters.forOTLPExporterWithin(s.exporterTo(gone.URL), time.Minute)
}

func (*sendSUT) exporterTo(rawURL string) config.Exporter {
	u, err := url.Parse(rawURL)
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	return config.Exporter{
		Endpoint: rawURL, URL: u, Gzip: true, QueueSize: 1000,
		LogInterval: 2 * time.Millisecond, TraceInterval: 2 * time.Millisecond,
	}
}

// ExportCallsWithASpan exports n calls, each with one span.
func (s *sendSUT) ExportCallsWithASpan(n int) {
	at := time.UnixMicro(1_700_000_000_000_000)
	for i := range n {
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{1, byte(i >> 8), byte(i)}, SpanID: trace.SpanID{2, byte(i >> 8), byte(i)}, TraceFlags: trace.FlagsSampled,
		})
		s.exporters.Call(export.Call{Trace: sc, At: at, Observed: at, Spans: []export.Span{{
			ID: sc.SpanID(), Name: "vendor call", Kind: trace.SpanKindClient, Start: at, End: at.Add(time.Millisecond),
		}}})
	}
}

// StopsAtOnceWithNoTimeLeft stops the exporter with no time left, and says
// whether the stop came back within a second.
func (s *sendSUT) StopsAtOnceWithNoTimeLeft() bool { return s.exporters.StopsAtOnceWithNoTimeLeft() }

// BrokenBodies is how many bodies the collector could not unzip, once the
// exporter has sent everything.
func (s *sendSUT) BrokenBodies() int {
	s.stopOnce()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broken
}

// RecordsArrived is how many log records the collector got, once the
// exporter has sent everything.
func (s *sendSUT) RecordsArrived() int {
	s.stopOnce()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records
}

// SpansArrived is how many spans the collector got, once the exporter has
// sent everything.
func (s *sendSUT) SpansArrived() int {
	s.stopOnce()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spans
}

func (s *sendSUT) stopOnce() {
	if !s.exporters.stopped {
		Expect(s.exporters.StopWithin(10 * time.Second)).WithOffset(2).To(Succeed())
	}
}

// receive is the collector: it unzips each body and counts the records or
// spans in it, and answers 400 to a body it cannot read.
func (s *sendSUT) receive(w http.ResponseWriter, r *http.Request) {
	body, err := unzip(r.Body)
	records, spans := 0, 0
	if err == nil && r.URL.Path == "/v1/logs" {
		in := &collogs.ExportLogsServiceRequest{}
		if err = proto.Unmarshal(body, in); err == nil {
			for _, rl := range in.GetResourceLogs() {
				for _, sl := range rl.GetScopeLogs() {
					records += len(sl.GetLogRecords())
				}
			}
		}
	}
	if err == nil && r.URL.Path == "/v1/traces" {
		in := &coltrace.ExportTraceServiceRequest{}
		if err = proto.Unmarshal(body, in); err == nil {
			for _, rs := range in.GetResourceSpans() {
				for _, ss := range rs.GetScopeSpans() {
					spans += len(ss.GetSpans())
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.broken++
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.records += records
	s.spans += spans
}

func unzip(body io.Reader) ([]byte, error) {
	zr, err := gzip.NewReader(body)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}
