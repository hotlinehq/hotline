// Package export sends records, their traces and the hotline.requests
// counter over OTLP.
// Every destination gets its own queue, so one that is down never holds up
// the others or the calls.
package export

import (
	"bytes"
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
)

// Timing says how long one send to a destination may take, retries
// included. How often each destination is sent to is in its config.
type Timing struct {
	Timeout time.Duration
}

// Service describes the proxy on every signal it sends.
type Service struct {
	Version    string
	InstanceID string
}

// Sink takes finished calls and sends them to every destination.
type Sink struct {
	destinations []destination
	// files are the folders every call is written to, one line per call.
	files []fileDestination
	// console, when set, prints every call to standard output.
	console *consoleDestination
	// lineResource is the resource every call line carries.
	lineResource []attribute.KeyValue
	// lines holds buffers to write a call line in.
	lines sync.Pool
	// scratch holds buffers to write a record or a call's spans in,
	// before they are copied to their own exact size.
	scratch sync.Pool
}

// destination is one exporter's own pipeline, with its own resource.
type destination struct {
	metrics  *sdkmetric.MeterProvider
	requests metric.Int64Counter
	// queue holds the written records and spans the written spans, both
	// sent with transport.
	queue     *queue[[]byte]
	spans     *queue[[]byte]
	transport *httpTransport
	// up is 1 while sends work, 0 from a failed send until one works, and
	// place names the destination in the proxy's own numbers.
	up    *atomic.Int64
	place attribute.Set
}

// fileDestination is one file exporter's queue of written lines and its
// writer.
type fileDestination struct {
	queue *queue[[]byte]
	up    *atomic.Int64
	place attribute.Set
}

// Call is one finished call: its record and its spans.
type Call struct {
	Trace trace.SpanContext
	At    time.Time
	// Attrs is the record every exporter gets.
	Attrs []attribute.KeyValue
	Spans []Span
	// Full, when set, is the record file exporters get instead: under
	// content: full, with every header, the bodies and its own signature.
	Full []attribute.KeyValue
	// Address is the address the call went to, as the app sent it, for
	// the console.
	Address string
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

// New starts one queue and sender per destination. Nothing is sent to a
// destination before the first record, so a destination that is down does
// not stop the proxy from starting. TLS to a destination trusts roots and
// the destination's own CAs.
//
// Only the config says how to send: every option is set here, so the
// library never falls back to the OTEL_EXPORTER_OTLP_* environment
// variables.
//
// Each file exporter takes its folder's lock, so New fails with
// ErrFolderTaken when another proxy writes to the same folder.
func New(exporters []config.Exporter, files []config.FileExporter, console *config.ConsoleExporter, out Console, roots *x509.CertPool, service Service, timing Timing, logger *slog.Logger, meter metric.Meter) (*Sink, error) {
	s := &Sink{}
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
		s.files = append(s.files, fileDestination{
			queue: newQueue("records", write, func(context.Context) error { return writer.close() }, setup),
			up:    up,
			place: attribute.NewSet(label),
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
		d := destination{
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
		s.destinations = append(s.destinations, d)
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
	observeDestinations(meter, s.destinations, s.local())
	return s, nil
}

// local are the destinations on this machine: the files and the console.
func (s *Sink) local() []fileDestination {
	if s.console == nil {
		return s.files
	}
	return append(slices.Clip(s.files), s.console.fileDestination)
}

// closeFiles lets go of the folders taken so far, when New fails.
func (s *Sink) closeFiles() {
	for _, f := range s.files {
		_ = f.queue.Shutdown(context.Background())
	}
}

// describe is the resource one destination gets: the proxy's own labels,
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

// A busy destination is tried again, first after about a second, then at
// most every few seconds, until the send's time is up.
const (
	retryFirst = time.Second
	retryMost  = 5 * time.Second
)

// metricSender sends a destination's metrics over HTTP.
func metricSender(e config.Exporter, roots *x509.CertPool, timing Timing) sdkmetric.Exporter {
	temporality := sdkmetric.DefaultTemporalitySelector
	if e.Delta {
		temporality = delta
	}
	return httpMetrics(e, tlsFor(e, roots), timing, temporality)
}

// tlsFor is the TLS setup for a destination, nil for a plain one.
func tlsFor(e config.Exporter, roots *x509.CertPool) *tls.Config {
	if e.URL.Scheme != "https" {
		return nil
	}
	return destinationTLS(e, roots)
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

// Call queues one call for every destination: its record, its count and
// its spans for each OTLP destination, one line for each file, and its
// lines on the console. It never
// blocks: a full queue refuses what is new.
func (s *Sink) Call(ctx context.Context, c Call) {
	if len(s.destinations) > 0 {
		s.record(ctx, c.At, c.Attrs)
		s.trace(c.Trace, c.Spans)
	}
	if s.console != nil {
		s.console.queue.add(s.console.lines(c))
	}
	if len(s.files) == 0 {
		return
	}
	attrs := c.Attrs
	if c.Full != nil {
		attrs = c.Full
	}
	buf := s.lines.Get().(*bytes.Buffer)
	buf.Reset()
	appendLine(buf, s.lineResource, c, attrs)
	line := slices.Clone(buf.Bytes())
	s.lines.Put(buf)
	for _, f := range s.files {
		f.queue.add(line)
	}
}

// record queues one hotline.request log record and counts the call. It
// never blocks: a full queue refuses the new record.
// The record is written once, for every destination.
func (s *Sink) record(ctx context.Context, at time.Time, attrs []attribute.KeyValue) {
	scratch := s.scratch.Get().(*[]byte)
	b := appendRecord((*scratch)[:0], at, time.Now(), level(attrs), trace.SpanContextFromContext(ctx), attrs)
	written := slices.Clone(b)
	*scratch = b
	s.scratch.Put(scratch)
	counter := make([]attribute.KeyValue, 0, 5)
	for _, kv := range attrs {
		if counted(kv) {
			counter = append(counter, kv)
		}
	}
	for _, d := range s.destinations {
		d.queue.add(written)
		d.requests.Add(ctx, 1, metric.WithAttributes(counter...))
	}
}

// level is INFO unless something went wrong: WARN when the call or the reply
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
func (s *Sink) Shutdown(ctx context.Context) error {
	var errs []error
	for _, d := range s.destinations {
		errs = append(errs, d.queue.Shutdown(ctx), d.spans.Shutdown(ctx), d.metrics.Shutdown(ctx))
		d.transport.close()
	}
	for _, f := range s.local() {
		errs = append(errs, f.queue.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
