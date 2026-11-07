// Package export sends records, their traces and the hotline.requests
// counter over OTLP.
// Every destination gets its own queue, so one that is down never holds up
// the others or the calls.
package export

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"google.golang.org/grpc/credentials"

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

// Sink takes finished records and sends them to every destination.
type Sink struct {
	destinations []destination
}

// destination is one exporter's own pipeline, with its own resource.
type destination struct {
	logs     *sdklog.LoggerProvider
	metrics  *sdkmetric.MeterProvider
	logger   log.Logger
	requests metric.Int64Counter
	queue    logQueue
	// spans is the queue of spans, and res the resource they carry.
	spans *queue[sdktrace.ReadOnlySpan]
	res   *resource.Resource
	// up is 1 while sends work, 0 from a failed send until one works, and
	// place names the destination in the proxy's own numbers.
	up    *atomic.Int64
	place attribute.Set
}

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
func New(exporters []config.Exporter, roots *x509.CertPool, service Service, timing Timing, logger *slog.Logger, meter metric.Meter) *Sink {
	s := &Sink{}
	stats := newSendStats(meter)
	for _, e := range exporters {
		logs, traces := grpcLogs, grpcTraces
		if e.HTTP {
			logs, traces = httpLogs, httpTraces
		}
		metrics := metricSender(e, roots, timing)
		up := &atomic.Int64{}
		up.Store(1)
		setup := queueSetup{endpoint: e.Endpoint, size: e.QueueSize, logger: logger, up: up}
		setup.sent, setup.dropped = stats.sent, stats.dropped
		logSetup := setup
		logSetup.labels = signal(e, "record")
		logSetup.interval = e.LogInterval
		q := newLogQueue(logs(e, tlsFor(e, roots), timing), logSetup)
		spanSetup := setup
		spanSetup.labels = signal(e, "span")
		spanSetup.interval = e.TraceInterval
		counted := countedMetrics{Exporter: metrics, sent: stats.sent, dropped: stats.dropped, labels: signal(e, "count"), up: up}
		res := describe(service, e.Resource)
		d := destination{
			logs: sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(q)),
			metrics: sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
				sdkmetric.WithReader(sdkmetric.NewPeriodicReader(counted,
					sdkmetric.WithInterval(e.MetricInterval),
					sdkmetric.WithTimeout(timing.Timeout)))),
			queue: q,
			spans: newSpanQueue(traces(e, tlsFor(e, roots), timing), spanSetup),
			res:   res,
			up:    up,
			place: attribute.NewSet(exporterLabel(e)),
		}
		d.logger = d.logs.Logger("hotline-proxy")
		// The name and unit are fixed and valid, so there is no error to handle.
		d.requests, _ = d.metrics.Meter("hotline-proxy").Int64Counter("hotline.requests", metric.WithUnit("{request}"))
		s.destinations = append(s.destinations, d)
	}
	observeDestinations(meter, s.destinations)
	return s
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

// metricSender sends a destination's metrics, over gRPC or HTTP.
func metricSender(e config.Exporter, roots *x509.CertPool, timing Timing) sdkmetric.Exporter {
	temporality := sdkmetric.DefaultTemporalitySelector
	if e.Delta {
		temporality = delta
	}
	send := grpcMetrics
	if e.HTTP {
		send = httpMetrics
	}
	return send(e, tlsFor(e, roots), timing, temporality)
}

// tlsFor is the TLS setup for a destination, nil for a plain one.
func tlsFor(e config.Exporter, roots *x509.CertPool) *tls.Config {
	if e.URL.Scheme != "https" {
		return nil
	}
	return destinationTLS(e, roots)
}

// grpcLogs sends records over gRPC. The config has checked the endpoint,
// the only thing New could refuse, so there is no error to handle.
func grpcLogs(e config.Exporter, tlsConfig *tls.Config, timing Timing) sdklog.Exporter {
	compressor := "none"
	if e.Gzip {
		compressor = "gzip"
	}
	logOptions := []otlploggrpc.Option{
		otlploggrpc.WithEndpoint(e.URL.Host),
		otlploggrpc.WithHeaders(e.Headers),
		otlploggrpc.WithCompressor(compressor),
		otlploggrpc.WithTimeout(timing.Timeout),
		otlploggrpc.WithRetry(otlploggrpc.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlploggrpc.WithInsecure(),
	}
	if tlsConfig != nil {
		logOptions[len(logOptions)-1] = otlploggrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig))
	}
	logs, _ := otlploggrpc.New(context.Background(), logOptions...)
	return logs
}

// grpcMetrics sends metrics over gRPC.
func grpcMetrics(e config.Exporter, tlsConfig *tls.Config, timing Timing, temporality sdkmetric.TemporalitySelector) sdkmetric.Exporter {
	compressor := "none"
	if e.Gzip {
		compressor = "gzip"
	}
	options := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(e.URL.Host),
		otlpmetricgrpc.WithHeaders(e.Headers),
		otlpmetricgrpc.WithCompressor(compressor),
		otlpmetricgrpc.WithTimeout(timing.Timeout),
		otlpmetricgrpc.WithRetry(otlpmetricgrpc.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlpmetricgrpc.WithTemporalitySelector(temporality),
		otlpmetricgrpc.WithInsecure(),
	}
	if tlsConfig != nil {
		options[len(options)-1] = otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(tlsConfig))
	}
	metrics, _ := otlpmetricgrpc.New(context.Background(), options...)
	return metrics
}

// httpLogs sends records over HTTP as protobuf, to /v1/logs after the
// endpoint's own path. The config has checked the endpoint, so there is no
// error to handle.
func httpLogs(e config.Exporter, tlsConfig *tls.Config, timing Timing) sdklog.Exporter {
	base := strings.TrimSuffix(e.URL.Path, "/")
	compression := otlploghttp.NoCompression
	if e.Gzip {
		compression = otlploghttp.GzipCompression
	}
	// A plain endpoint still sets a TLS config, nil, so none is read from
	// the environment.
	options := []otlploghttp.Option{
		otlploghttp.WithEndpoint(e.URL.Host),
		otlploghttp.WithURLPath(base + "/v1/logs"),
		otlploghttp.WithHeaders(e.Headers),
		otlploghttp.WithCompression(compression),
		otlploghttp.WithTimeout(timing.Timeout),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: true, InitialInterval: retryFirst, MaxInterval: retryMost, MaxElapsedTime: timing.Timeout}),
		otlploghttp.WithTLSClientConfig(tlsConfig),
	}
	if tlsConfig == nil {
		options = append(options, otlploghttp.WithInsecure())
	}
	logs, _ := otlploghttp.New(context.Background(), options...)
	return logs
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
	metrics, _ := otlpmetrichttp.New(context.Background(), options...)
	return metrics
}

func delta(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

// Record queues one hotline.request log record and counts the call. It never
// blocks: a full queue drops its oldest record.
func (s *Sink) Record(ctx context.Context, at time.Time, attrs []attribute.KeyValue) {
	var r log.Record
	r.SetEventName("hotline.request")
	r.SetTimestamp(at)
	severity := level(attrs)
	r.SetSeverity(severity)
	r.SetSeverityText(severity.String())
	// One call to add them all, so the record's list is made once.
	r.AddAttributes(attrs...)
	counter := make([]attribute.KeyValue, 0, 5)
	for _, kv := range attrs {
		if counted(kv) {
			counter = append(counter, kv)
		}
	}
	for _, d := range s.destinations {
		d.logger.Emit(ctx, r)
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
		errs = append(errs, d.logs.Shutdown(ctx), d.spans.Shutdown(ctx), d.metrics.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
