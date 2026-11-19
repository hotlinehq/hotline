// Package export sends records, their traces and the hotline.requests
// counter over OTLP.
// Every exporter gets its own queue, so one that is down never holds up
// the others or the calls.
package export

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/labels"
)

// Timing says how long one send to an exporter may take, retries
// included. How often each exporter is sent to is in its config.
type Timing struct {
	Timeout time.Duration
}

// Service describes the proxy on every signal it sends.
type Service struct {
	Version    string
	InstanceID string
}

// Exporters takes finished calls and sends them to every exporter.
type Exporters struct {
	exporters []exporter
	// files are the folders every call is written to, one line per call.
	files []fileExporter
	// console, when set, prints every call to standard output.
	console *consoleExporter
	// batches hold the ledger's batch records waiting for each
	// exporter that takes records, the OTLP ones first, then the files.
	batches []*batchLine
	// lineResource is the resource every call line carries.
	lineResource []attribute.KeyValue
	// lines holds buffers to write a call line in.
	lines sync.Pool
	// scratch holds buffers to write a record or a call's spans in,
	// before they are copied to their own exact size.
	scratch sync.Pool
	// labels keeps the records counter's label sets, made once each.
	labels labels.Kept
}

// exporter is the pipeline of one otlp exporter, with its own resource.
type exporter struct {
	metrics  *sdkmetric.MeterProvider
	requests metric.Int64Counter
	// queue holds the written records and spans the written spans, both
	// sent with transport.
	queue     *queue[[]byte]
	spans     *queue[[]byte]
	transport *httpTransport
	// up is 1 while sends work, 0 from a failed send until one works, and
	// place names the exporter in the proxy's own numbers.
	up    *atomic.Int64
	place attribute.Set
	// batches sends the ledger's batch records to the exporter, and
	// notes the records its queue dropped.
	batches *batchLine
}

// fileExporter is one file exporter's queue of written lines and its
// writer.
type fileExporter struct {
	queue   *queue[[]byte]
	writer  *fileWriter
	up      *atomic.Int64
	place   attribute.Set
	batches *batchLine
}

// Call is one finished call, or a WebSocket session: its record and its
// spans.
type Call struct {
	// Event is the record's event name: empty for a call, which is
	// hotline.request, or SessionEvent for a WebSocket session. Only a
	// call counts in hotline.requests (Peter, 2026-11-18).
	Event string
	Trace trace.SpanContext
	At    time.Time
	// Attrs is the record every exporter gets.
	Attrs []attribute.KeyValue
	Spans []Span
	// Content, under content: full, is what file exporters get besides
	// the record: every header and the bodies, under hotline.content.*
	// names.
	Content []attribute.KeyValue
	// Ledger is the record's place in its ledger batch. An exporter
	// that drops the record lists it there in the batch's record.
	Ledger Entry
}

// Entry is a record's place in its ledger batch: the batch, its index in
// it, and its hash.
type Entry struct {
	Batch string
	Index int
	Hash  string
}

// fileInterval is how long a call line waits at most before it is
// written: short, so a line shows up at once, yet long enough to write
// many lines in one go under load (Peter, 2026-11-11).
const fileInterval = 100 * time.Millisecond

// fileQueueSize is how many calls wait for a file exporter at most.
const fileQueueSize = 10_000

// counted are the record attributes the hotline.requests counter carries.
func counted(kv attribute.KeyValue) bool {
	switch kv.Key {
	case "hotline.integration.id", semconv.URLTemplateKey, semconv.HTTPRequestMethodKey,
		semconv.HTTPResponseStatusCodeKey, semconv.ErrorTypeKey:
		return true
	}
	return false
}

// Setup is what New needs: the exporters from the config, where the
// console writes, the roots TLS trusts, and how the proxy describes and
// times itself.
type Setup struct {
	// OTLP are the otlp exporters, Files the file exporters.
	OTLP  []config.Exporter
	Files []config.FileExporter
	// Console is nil when the config has no console exporter.
	Console *config.ConsoleExporter
	Out     Console
	Roots   *x509.CertPool
	Service Service
	Timing  Timing
	Logger  *slog.Logger
	Meter   metric.Meter
}

// New starts one queue and sender per exporter. Nothing is sent to a
// exporter before the first record, so an exporter that is down does
// not stop the proxy from starting. TLS to an exporter trusts roots and
// the exporter's own CAs.
//
// Only the config says how to send: every option is set here, so the
// library never falls back to the OTEL_EXPORTER_OTLP_* environment
// variables.
//
// Each file exporter takes its folder's lock, so New fails with
// ErrFolderTaken when another proxy writes to the same folder.
func New(setup Setup) (*Exporters, error) {
	exporters, files, console, out := setup.OTLP, setup.Files, setup.Console, setup.Out
	roots, service, timing, logger, meter := setup.Roots, setup.Service, setup.Timing, setup.Logger, setup.Meter
	s := &Exporters{}
	s.scratch.New = func() any {
		b := make([]byte, 0, 4096)
		return &b
	}
	s.lines.New = func() any { return &bytes.Buffer{} }
	stats := newSendStats(meter)
	s.lineResource = describe(service, nil).Attributes()
	for _, e := range files {
		writer, err := openFileWriter(e)
		if err != nil {
			s.closeFiles()
			return nil, err
		}
		up := &atomic.Int64{}
		up.Store(1)
		label := attribute.String("hotline.exporter", e.Path)
		setup := queueSetup{
			endpoint: e.Path, size: fileQueueSize, memory: e.QueueMemory, interval: fileInterval, logger: logger, up: up,
			sent: stats.sent, dropped: stats.dropped, labels: attribute.NewSet(label, attribute.String("hotline.signal", "record")),
		}
		write := func(_ context.Context, lines [][]byte) error { return writer.write(lines) }
		s.files = append(s.files, fileExporter{
			queue:  newQueue("records", write, func(context.Context) error { return writer.close() }, setup),
			writer: writer,
			up:     up,
			place:  attribute.NewSet(label),
		})
	}
	for _, e := range exporters {
		t := newHTTPTransport(e, tlsFor(e, roots), service.Version)
		metrics := metricSender(e, roots, timing)
		up := &atomic.Int64{}
		up.Store(1)
		setup := queueSetup{endpoint: e.Endpoint, size: e.QueueSize, logger: logger, up: up}
		setup.sent, setup.dropped = stats.sent, stats.dropped
		logSetup := setup
		logSetup.labels = signal(e, "record")
		logSetup.interval = e.LogInterval
		spanSetup := setup
		spanSetup.labels = signal(e, "span")
		spanSetup.interval = e.TraceInterval
		counted := countedMetrics{Exporter: metrics, sent: stats.sent, dropped: stats.dropped, labels: signal(e, "count"), up: up}
		res := describe(service, e.Resource)
		written := resourceOf(res.Attributes())
		d := exporter{
			metrics: sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(counted,
					sdkmetric.WithInterval(e.MetricInterval),
					sdkmetric.WithTimeout(timing.Timeout)))),
			queue:     newItemQueue("records", records, t, written, timing.Timeout, logSetup),
			spans:     newItemQueue("spans", spans, t, written, timing.Timeout, spanSetup),
			transport: t,
			up:        up,
			place:     attribute.NewSet(exporterLabel(e)),
		}
		// The name and unit are fixed and valid, so there is no error to handle.
		d.requests, _ = d.metrics.Meter("hotline-proxy").Int64Counter("hotline.requests", metric.WithUnit("{request}"))
		// The batch line sends records too, at the same time as the record
		// queue, so it has its own zipper.
		batchZip := t.newZipper()
		d.batches = newBatchLine(e.Endpoint, logger, d.queue.ForceFlush, func(ctx context.Context, items [][]byte) error {
			body := appendBatch(nil, written, items)
			return sendWithRetry(ctx, t, records, batchZip, body, timing.Timeout)
		}, func(b Batch, attrs []attribute.KeyValue) []byte {
			return appendRecord(nil, BatchEvent, b.At, time.Now(), log.SeverityInfo, trace.SpanContext{}, attrs)
		})
		s.exporters = append(s.exporters, d)
		s.batches = append(s.batches, d.batches)
	}
	for i := range s.files {
		f := &s.files[i]
		f.batches = newBatchLine(f.writer.e.Path, logger, f.queue.ForceFlush, func(_ context.Context, items [][]byte) error {
			return f.writer.write(items)
		}, func(b Batch, attrs []attribute.KeyValue) []byte {
			buf := &bytes.Buffer{}
			appendBatchLine(buf, s.lineResource, b.At, attrs)
			return buf.Bytes()
		})
		s.batches = append(s.batches, f.batches)
	}
	if console != nil {
		up := &atomic.Int64{}
		up.Store(1)
		label := attribute.String("hotline.exporter", "console")
		s.console = newConsole(console, out, queueSetup{
			endpoint: "console", size: consoleQueueSize, interval: fileInterval, eager: true, logger: logger, up: up,
			sent: stats.sent, dropped: stats.dropped, labels: attribute.NewSet(label, attribute.String("hotline.signal", "record")),
		})
	}
	observeExporters(meter, s.exporters, s.local())
	return s, nil
}

// local are the exporters on this machine: the files and the console.
func (s *Exporters) local() []fileExporter {
	if s.console == nil {
		return s.files
	}
	return append(slices.Clip(s.files), s.console.fileExporter)
}

// closeFiles lets go of the folders taken so far, when New fails.
func (s *Exporters) closeFiles() {
	for _, f := range s.files {
		_ = f.queue.Shutdown(context.Background())
	}
}

// describe is the resource one exporter gets: the proxy's own labels,
// with the config's labels on top. The config has refused service.version.
func describe(service Service, labels map[string]string) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName("hotline-proxy"),
		semconv.ServiceVersion(service.Version),
		semconv.ServiceInstanceID(service.InstanceID),
	}
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		attrs = append(attrs, attribute.String(name, labels[name]))
	}
	// A later label with the same name wins.
	return resource.NewSchemaless(attrs...)
}

// A busy exporter is tried again, first after about a second, then at
// most every few seconds, until the send's time is up.
const (
	retryFirst = time.Second
	retryMost  = 5 * time.Second
)

// metricSender sends an exporter's metrics over HTTP.
func metricSender(e config.Exporter, roots *x509.CertPool, timing Timing) sdkmetric.Exporter {
	temporality := sdkmetric.DefaultTemporalitySelector
	if e.Delta {
		temporality = delta
	}
	return httpMetrics(e, tlsFor(e, roots), timing, temporality)
}

// tlsFor is the TLS setup for an exporter, nil for a plain one.
func tlsFor(e config.Exporter, roots *x509.CertPool) *tls.Config {
	if e.URL.Scheme != "https" {
		return nil
	}
	return exporterTLS(e, roots)
}

// httpMetrics sends metrics over HTTP as protobuf, to /v1/metrics after the
// endpoint's own path.
func httpMetrics(e config.Exporter, tlsConfig *tls.Config, timing Timing, temporality sdkmetric.TemporalitySelector) sdkmetric.Exporter {
	base := strings.TrimSuffix(e.URL.Path, "/")
	compression := otlpmetrichttp.NoCompression
	if e.Gzip {
		compression = otlpmetrichttp.GzipCompression
	}
	options := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(e.URL.Host),
		otlpmetrichttp.WithURLPath(base + "/v1/metrics"),
		otlpmetrichttp.WithHeaders(e.Headers),
		otlpmetrichttp.WithCompression(compression),
		otlpmetrichttp.WithTimeout(timing.Timeout),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlpmetrichttp.WithTemporalitySelector(temporality),
		otlpmetrichttp.WithTLSClientConfig(tlsConfig),
	}
	if tlsConfig == nil {
		options = append(options, otlpmetrichttp.WithInsecure())
	}
	if e.H2C {
		// The library's own client cannot speak h2c.
		options = append(options, otlpmetrichttp.WithHTTPClient(httpClient(e, tlsConfig)))
	}
	metrics, _ := otlpmetrichttp.New(context.Background(), options...)
	return metrics
}

func delta(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

// Call queues one call for every exporter: its record, its count and
// its spans for each OTLP exporter, one line for each file, and its
// lines on the console. It never blocks: a full queue refuses what is new,
// and an exporter that keeps the ledger notes the record it dropped, to
// list it in the record of the record's batch.
func (s *Exporters) Call(ctx context.Context, c Call) {
	if len(s.exporters) > 0 {
		s.record(ctx, c, c.Attrs)
		s.trace(c.Trace, c.Spans)
	}
	// The file exporters' line, with every header when content is full.
	var line []byte
	if len(s.files) > 0 {
		line = s.line(c, true)
		for _, f := range s.files {
			if !f.queue.add(line) {
				f.batches.dropped(c.Ledger)
			}
		}
	}
	switch {
	case s.console == nil:
	case s.console.out.JSON:
		// The record every exporter gets, never content: full. It is the
		// file exporters' line when that has no more.
		if line == nil || c.Content != nil {
			line = s.line(c, false)
		}
		s.console.queue.add(terminalSafe(line))
	default:
		s.console.queue.add(s.console.lines(c))
	}
}

// line is the call as one line of JSON.
func (s *Exporters) line(c Call, withContent bool) []byte {
	buf := s.lines.Get().(*bytes.Buffer)
	buf.Reset()
	appendLine(buf, s.lineResource, c, withContent)
	line := slices.Clone(buf.Bytes())
	s.lines.Put(buf)
	return line
}

// event is the record's event name.
func (c Call) event() string { return cmp.Or(c.Event, CallEvent) }

// record queues one log record and counts it when it is a call. It
// never blocks: a full queue refuses the new record. It adds to taken
// whether each exporter took it.
// The record is written once, for every exporter.
func (s *Exporters) record(ctx context.Context, c Call, attrs []attribute.KeyValue) {
	scratch := s.scratch.Get().(*[]byte)
	b := appendRecord((*scratch)[:0], c.event(), c.At, time.Now(), level(attrs), trace.SpanContextFromContext(ctx), attrs)
	written := slices.Clone(b)
	*scratch = b
	s.scratch.Put(scratch)
	// Room for the five on the stack, so the list costs no memory to make.
	counter := make([]attribute.KeyValue, 0, 5)
	for _, kv := range attrs {
		if counted(kv) {
			counter = append(counter, kv)
		}
	}
	// One set for every exporter.
	counts := metric.WithAttributeSet(s.labels.Set(counter...))
	call := c.event() == CallEvent
	for _, d := range s.exporters {
		if !d.queue.add(written) {
			d.batches.dropped(c.Ledger)
		}
		if call {
			d.requests.Add(ctx, 1, counts)
		}
	}
}

// level is INFO unless something went wrong: WARN when the call or the response
// broke the spec or the vendor refused the call (a 4xx), ERROR when the
// vendor failed (a 5xx) or the call failed in the proxy.
func level(attrs []attribute.KeyValue) log.Severity {
	severity := log.SeverityInfo
	for _, kv := range attrs {
		switch kv.Key {
		case semconv.ErrorTypeKey:
			return log.SeverityError
		case semconv.HTTPResponseStatusCodeKey:
			switch status := kv.Value.AsInt64(); {
			case status >= 500:
				return log.SeverityError
			case status >= 400:
				severity = log.SeverityWarn
			}
		case "hotline.schema.request.errors", "hotline.schema.response.errors":
			severity = log.SeverityWarn
		}
	}
	return severity
}

// Shutdown sends what is still queued, within the time ctx allows.
func (s *Exporters) Shutdown(ctx context.Context) error {
	var errs []error
	// The batch records go after the records they cover.
	for _, b := range s.batches {
		errs = append(errs, b.shutdown(ctx))
	}
	for _, d := range s.exporters {
		errs = append(errs, d.queue.Shutdown(ctx), d.spans.Shutdown(ctx), d.metrics.Shutdown(ctx))
		d.transport.close()
	}
	for _, f := range s.local() {
		errs = append(errs, f.queue.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
