package export

// SPIKE: compares today's library path with the throwaway encoder in
// internal/otlpwire (docs/otlp-encoder-scope.md). Not for merging.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	kgzip "github.com/klauspost/compress/gzip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/otlpwire"
)

// spikeCall is about what the load test's small call records: the same
// attributes as record_bench_test.go, and the spans of a full call.
func spikeCall(withErrors bool) (trace.SpanContext, time.Time, []attribute.KeyValue, []Span) {
	at := time.Unix(1_700_000_000, 123456789)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	attrs := []attribute.KeyValue{
		attribute.String("hotline.integration.id", "load-test"),
		attribute.String("hotline.ingress", "forward_proxy"),
		attribute.String("client.address", "127.0.0.1"),
		attribute.String("server.address", "api.load.test"),
		attribute.String("http.request.method", "PUT"),
		attribute.Int("http.response.status_code", 200),
		attribute.String("network.peer.address", "127.0.0.1"),
		attribute.Int64("hotline.timing.total_us", 812),
		attribute.String("url.scheme", "https"),
		attribute.Int("server.port", 8443),
		attribute.Int("http.request.body.size", 120),
		attribute.Bool("hotline.client.cancelled", false),
		attribute.String("hotline.config.version", "hotline-proxy.yaml+0123456789ab"),
		attribute.String("url.full", "https://api.load.test/v1/sessions/s-1?dry=true"),
		attribute.String("url.template", "/v1/sessions/{id}"),
		attribute.String("hotline.request.body.signature", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="),
		attribute.String("hotline.response.body.signature", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="),
		attribute.String("network.protocol.version", "1.1"),
		attribute.Int("http.response.body.size", 340),
		attribute.String("tls.protocol.version", "1.3"),
		attribute.String("tls.server.not_after", "2027-01-01T00:00:00Z"),
		attribute.StringSlice("http.request.header.content-type", []string{"application/json"}),
		attribute.StringSlice("http.response.header.content-type", []string{"application/json"}),
		attribute.String("hotline.schema.status", "valid"),
		attribute.String("hotline.trace.signature", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="),
		attribute.String("hotline.signature_key_id", "k1"),
		attribute.String("hotline.signature", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="),
	}
	if withErrors {
		attrs = append(attrs,
			attribute.Slice("hotline.schema.request.errors",
				attribute.MapValue(attribute.String("part", "body"), attribute.String("rule", "type"), attribute.String("place", "$.items[0].quantity")),
				attribute.MapValue(attribute.String("part", "query"), attribute.String("rule", "required"), attribute.String("place", "page")),
			),
			attribute.Int("hotline.schema.request.errors_total", 22),
			attribute.Float64("spike.float", 1.5),
			attribute.Int64Slice("spike.ints", []int64{1, -2, 300}),
			attribute.BoolSlice("spike.bools", []bool{true, false}),
			attribute.String("spike.long", string(bytes.Repeat([]byte("x"), 300))),
			attribute.String("spike.empty", ""),
		)
	}
	call := trace.SpanID{9, 9, 9, 9, 9, 9, 9, 9}
	vendor := trace.SpanID{5, 5, 5, 5, 5, 5, 5, 5}
	us := func(n int) time.Time { return at.Add(time.Duration(n) * time.Microsecond) }
	spans := []Span{
		{ID: call, Parent: trace.SpanID{7, 7, 7, 7, 7, 7, 7, 7}, Name: "hotline call", Kind: trace.SpanKindServer, Start: at, End: us(812), Failed: withErrors},
		{ID: trace.SpanID{6, 6, 6, 6, 6, 6, 6, 1}, Parent: call, Name: "routing", Kind: trace.SpanKindInternal, Start: at, End: us(20)},
		{ID: vendor, Parent: call, Name: "vendor call", Kind: trace.SpanKindClient, Start: us(20), End: us(700),
			Attrs: []attribute.KeyValue{attribute.Bool("hotline.connection.reused", true)}},
		{ID: trace.SpanID{6, 6, 6, 6, 6, 6, 6, 2}, Parent: vendor, Name: "send call", Kind: trace.SpanKindInternal, Start: us(20), End: us(40)},
		{ID: trace.SpanID{6, 6, 6, 6, 6, 6, 6, 3}, Parent: vendor, Name: "wait for vendor", Kind: trace.SpanKindInternal, Start: us(40), End: us(650)},
		{ID: trace.SpanID{6, 6, 6, 6, 6, 6, 6, 4}, Parent: vendor, Name: "receive reply", Kind: trace.SpanKindInternal, Start: us(650), End: us(700)},
		{ID: trace.SpanID{6, 6, 6, 6, 6, 6, 6, 5}, Parent: call, Name: "reply to app", Kind: trace.SpanKindInternal, Start: us(700), End: us(812)},
		{ID: trace.SpanID{1, 1, 1, 1, 1, 1, 1, 1}, Parent: call, Name: "request spec check", Kind: trace.SpanKindInternal, Start: us(40), End: us(300), OK: true},
		{ID: trace.SpanID{2, 2, 2, 2, 2, 2, 2, 2}, Parent: trace.SpanID{1, 1, 1, 1, 1, 1, 1, 1}, Name: "wait for check worker", Kind: trace.SpanKindInternal, Start: us(40), End: us(45)},
		{ID: trace.SpanID{3, 3, 3, 3, 3, 3, 3, 3}, Parent: call, Name: "reply spec check", Kind: trace.SpanKindInternal, Start: us(700), End: us(900), OK: true},
		{ID: trace.SpanID{4, 4, 4, 4, 4, 4, 4, 4}, Parent: trace.SpanID{3, 3, 3, 3, 3, 3, 3, 3}, Name: "wait for check worker", Kind: trace.SpanKindInternal, Start: us(700), End: us(705),
			Failed: withErrors, Attrs: []attribute.KeyValue{attribute.String("error.type", "overloaded")}},
	}
	if !withErrors {
		spans[0].Parent = trace.SpanID{}
	}
	return sc, at, attrs, spans
}

// receiver keeps what reaches /v1/logs and /v1/traces, unzipped, or drops
// it when keep is false.
type receiver struct {
	keep   bool
	mu     sync.Mutex
	logs   [][]byte
	traces [][]byte
}

func (rv *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !rv.keep {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/x-protobuf")
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		body, _ = gzip.NewReader(r.Body)
	}
	data, _ := io.ReadAll(body)
	rv.mu.Lock()
	defer rv.mu.Unlock()
	switch r.URL.Path {
	case "/v1/logs":
		rv.logs = append(rv.logs, data)
	case "/v1/traces":
		rv.traces = append(rv.traces, data)
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
}

func spikeExporter(t testing.TB, server *httptest.Server) config.Exporter {
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return config.Exporter{
		Endpoint: server.URL, HTTP: true, URL: u, Gzip: true, QueueSize: 1 << 20,
		MetricInterval: time.Hour, LogInterval: time.Hour, TraceInterval: time.Hour,
		Resource: map[string]string{"deployment.environment.name": "spike"},
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func spikeService() Service { return Service{Version: "spike", InstanceID: "0000-instance"} }

// wire is the new path for one destination: records and spans encoded once
// into their own buffers, then joined, gzipped and posted per batch.
type wire struct {
	client   *http.Client
	base     string
	resource []byte
	records  [][]byte
	spans    [][]byte
	batch    []byte
	zipped   bytes.Buffer
	zip      zipper
	pool     sync.Pool
}

// zipper is a gzip writer, from the standard library or klauspost.
type zipper interface {
	io.WriteCloser
	Reset(io.Writer)
}

func newWire(base string, d destination, level int, fast ...bool) *wire {
	attrs := d.res.Attributes()
	w := &wire{client: &http.Client{Timeout: 10 * time.Second}, base: base, resource: otlpwire.Resource(attrs)}
	if len(fast) > 0 && fast[0] {
		w.zip, _ = kgzip.NewWriterLevel(&w.zipped, level)
	} else {
		w.zip, _ = gzip.NewWriterLevel(&w.zipped, level)
	}
	w.pool.New = func() any { b := make([]byte, 0, 2048); return &b }
	return w
}

func (w *wire) buf() []byte { return (*w.pool.Get().(*[]byte))[:0] }

func (w *wire) record(at time.Time, sc trace.SpanContext, attrs []attribute.KeyValue) {
	severity := level(attrs)
	w.records = append(w.records, otlpwire.AppendRecord(w.buf(), otlpwire.Record{
		At: at, Observed: at, Severity: int(severity), SeverityText: severity.String(),
		EventName: "hotline.request", Context: sc, Attrs: attrs,
	}))
	if len(w.records) >= batchSize {
		w.send("/v1/logs", &w.records)
	}
}

func (w *wire) trace(sc trace.SpanContext, spans []Span) {
	for _, s := range spans {
		status := 0
		if s.Failed {
			status = 2
		}
		if s.OK {
			status = 1
		}
		w.spans = append(w.spans, otlpwire.AppendSpan(w.buf(), otlpwire.Span{
			Trace: sc, ID: s.ID, Parent: s.Parent, Name: s.Name, Kind: s.Kind,
			Start: s.Start, End: s.End, Status: status, Attrs: s.Attrs,
		}))
		if len(w.spans) >= batchSize {
			w.send("/v1/traces", &w.spans)
		}
	}
}

func (w *wire) send(path string, items *[][]byte) {
	if len(*items) == 0 {
		return
	}
	w.batch = otlpwire.Batch(w.batch[:0], w.resource, "hotline-proxy", *items)
	for _, b := range *items {
		w.pool.Put(&b)
	}
	*items = (*items)[:0]
	w.zipped.Reset()
	w.zip.Reset(&w.zipped)
	_, _ = w.zip.Write(w.batch)
	_ = w.zip.Close()
	req, _ := http.NewRequest(http.MethodPost, w.base+path, bytes.NewReader(w.zipped.Bytes()))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := w.client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

func (w *wire) flush() {
	w.send("/v1/logs", &w.records)
	w.send("/v1/traces", &w.spans)
}

// TestSpikeSameAsLibrary sends the same calls both ways and compares what
// the official OTLP types decode from each.
func TestSpikeSameAsLibrary(t *testing.T) {
	for _, withErrors := range []bool{false, true} {
		old, neu := &receiver{keep: true}, &receiver{keep: true}
		oldServer, newServer := httptest.NewServer(old), httptest.NewServer(neu)
		sink := New([]config.Exporter{spikeExporter(t, oldServer)}, nil, spikeService(), Timing{Timeout: 10 * time.Second}, quiet, noop.Meter{})
		w := newWire(newServer.URL, sink.destinations[0], gzip.DefaultCompression)
		sc, at, attrs, spans := spikeCall(withErrors)
		for range 3 {
			sink.Record(trace.ContextWithSpanContext(context.Background(), sc), at, attrs)
			sink.Trace(sc, spans)
			w.record(at, sc, attrs)
			w.trace(sc, spans)
		}
		_ = sink.Shutdown(context.Background())
		w.flush()
		oldServer.Close()
		newServer.Close()

		var oldLogs, newLogs collogs.ExportLogsServiceRequest
		mustDecode(t, old.logs, &oldLogs)
		mustDecode(t, neu.logs, &newLogs)
		// The library stamps the observed time with the clock; the new
		// path uses the call's time. Only the presence is compared.
		for _, req := range []*collogs.ExportLogsServiceRequest{&oldLogs, &newLogs} {
			for _, r := range req.ResourceLogs[0].ScopeLogs[0].LogRecords {
				if r.ObservedTimeUnixNano == 0 {
					t.Fatal("no observed time")
				}
				r.ObservedTimeUnixNano = 1
			}
		}
		if !proto.Equal(&oldLogs, &newLogs) {
			t.Fatalf("logs differ (errors %v)\nold: %v\nnew: %v", withErrors, &oldLogs, &newLogs)
		}

		var oldTraces, newTraces coltrace.ExportTraceServiceRequest
		mustDecode(t, old.traces, &oldTraces)
		mustDecode(t, neu.traces, &newTraces)
		// The library's batch has no fixed order of spans.
		for _, req := range []*coltrace.ExportTraceServiceRequest{&oldTraces, &newTraces} {
			s := req.ResourceSpans[0].ScopeSpans[0].Spans
			sort.SliceStable(s, func(i, j int) bool { return bytes.Compare(s[i].SpanId, s[j].SpanId) < 0 })
		}
		if !proto.Equal(&oldTraces, &newTraces) {
			t.Fatalf("traces differ (errors %v)\nold: %v\nnew: %v", withErrors, &oldTraces, &newTraces)
		}
	}
}

func mustDecode(t *testing.T, bodies [][]byte, m proto.Message) {
	t.Helper()
	if len(bodies) != 1 {
		t.Fatalf("got %d sends, want 1", len(bodies))
	}
	if err := proto.Unmarshal(bodies[0], m); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkSpikeLibrary is today's path for one call: the record and the
// call's spans through the OpenTelemetry libraries, a share of the batch
// sends included, over HTTP with gzip to a receiver that drops them.
func BenchmarkSpikeLibrary(b *testing.B) {
	server := httptest.NewServer(&receiver{})
	defer server.Close()
	sink := New([]config.Exporter{spikeExporter(b, server)}, nil, spikeService(), Timing{Timeout: 10 * time.Second}, quiet, noop.Meter{})
	d := sink.destinations[0]
	sc, at, attrs, spans := spikeCall(false)
	ctx := context.Background()
	b.ReportAllocs()
	n := 0
	from := cpuTime()
	defer reportCPU(b, from)
	for b.Loop() {
		sink.Record(trace.ContextWithSpanContext(ctx, sc), at, attrs)
		sink.Trace(sc, spans)
		if n++; n%batchSize == 0 {
			_ = d.queue.ForceFlush(ctx)
			_ = d.spans.ForceFlush(ctx)
		}
	}
	_ = sink.Shutdown(ctx)
}

// BenchmarkSpikeWire is the same call through the new encoder. The counter
// still goes through the metrics library, as planned.
func BenchmarkSpikeWire(b *testing.B) {
	for _, c := range []struct {
		name  string
		level int
		fast  bool
	}{{"gzip-default", gzip.DefaultCompression, false}, {"gzip-fastest", gzip.BestSpeed, false},
		{"klauspost-default", gzip.DefaultCompression, true}, {"klauspost-fastest", gzip.BestSpeed, true}} {
		b.Run(c.name, func(b *testing.B) {
			server := httptest.NewServer(&receiver{})
			defer server.Close()
			sink := New([]config.Exporter{spikeExporter(b, server)}, nil, spikeService(), Timing{Timeout: 10 * time.Second}, quiet, noop.Meter{})
			d := sink.destinations[0]
			w := newWire(server.URL, d, c.level, c.fast)
			sc, at, attrs, spans := spikeCall(false)
			ctx := context.Background()
			b.ReportAllocs()
			from := cpuTime()
			defer reportCPU(b, from)
			for b.Loop() {
				counter := make([]attribute.KeyValue, 0, 5)
				for _, kv := range attrs {
					if counted(kv) {
						counter = append(counter, kv)
					}
				}
				d.requests.Add(ctx, 1, metric.WithAttributes(counter...))
				w.record(at, sc, attrs)
				w.trace(sc, spans)
			}
			w.flush()
			_ = sink.Shutdown(ctx)
		})
	}
}

// cpuTime is the processor time the whole process has used so far, the
// garbage collector and the sending goroutines included.
func cpuTime() time.Duration {
	var u syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &u)
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}

func reportCPU(b *testing.B, from time.Duration) {
	b.ReportMetric(float64(cpuTime()-from)/float64(b.N), "cpu-ns/op")
}
