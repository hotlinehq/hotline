package export

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"hotline-proxy/internal/config"
)

// Own sends the numbers the proxy keeps about itself to the telemetry
// exporter, and nowhere else. Without one they are not measured at all.
type Own struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
}

// NewOwn starts the pipeline of the proxy's own numbers, with the Go
// runtime's among them. A metric telemetry.disable names is dropped before
// it is measured. With no telemetry section, nothing is measured.
func NewOwn(t *config.Telemetry, roots *x509.CertPool, service Service, timing Timing) *Own {
	if t == nil {
		return &Own{meter: noop.NewMeterProvider().Meter("hotline-proxy")}
	}
	options := []sdkmetric.Option{
		sdkmetric.WithResource(describe(service, t.Exporter.Resource)),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricSender(t, roots, timing),
			sdkmetric.WithInterval(t.MetricInterval),
			sdkmetric.WithTimeout(timing.Timeout))),
	}
	// Five of the runtime's numbers are enough (Peter, 2026-11-01), with
	// go.memory.allocated added for the load test (Peter, 2026-11-02).
	for _, name := range append(slices.Clone(t.Disable), "go.memory.allocations",
		"go.processor.limit", "go.config.gogc") {
		options = append(options, sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: name},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationDrop{}})))
	}
	provider := sdkmetric.NewMeterProvider(options...)
	// The runtime's instruments are fixed and valid, so there is no error
	// to handle.
	_ = runtime.Start(runtime.WithMeterProvider(provider))
	return &Own{provider: provider, meter: provider.Meter("hotline-proxy")}
}

// Meter makes the instruments of the proxy's own numbers.
func (o *Own) Meter() metric.Meter {
	return o.meter
}

// Shutdown sends the last numbers, within the time ctx allows.
func (o *Own) Shutdown(ctx context.Context) error {
	if o.provider == nil {
		return nil
	}
	return o.provider.Shutdown(ctx)
}

// exporterTLS trusts roots and the exporter's own certificate authorities, and shows
// its client certificate, if any.
func exporterTLS(e config.Exporter, roots *x509.CertPool) *tls.Config {
	pool := roots.Clone()
	pool.AppendCertsFromPEM(e.CAs)
	c := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if e.Certificate != nil {
		c.Certificates = []tls.Certificate{*e.Certificate}
	}
	return c
}

// sendStats are the instruments that count what each exporter's queues
// and senders did.
type sendStats struct {
	sent    metric.Int64Counter
	dropped metric.Int64Counter
}

// newSendStats makes the counters. Their names and units are fixed and
// valid, so there is no error to handle.
func newSendStats(meter metric.Meter) sendStats {
	var s sendStats
	s.sent, _ = meter.Int64Counter("hotline.exporter.sent", metric.WithUnit("{item}"),
		metric.WithDescription("Records, spans or counts delivered to this exporter."))
	s.dropped, _ = meter.Int64Counter("hotline.exporter.dropped", metric.WithUnit("{item}"),
		metric.WithDescription("Records, spans or counts lost on the way to this exporter: queue_full when the queue "+
			"overflowed, send_failed when the receiver refused the send or could not be reached."))
	return s
}

// observeExporters sends, for every exporter, how many items wait in
// its queues and whether its last send worked.
func observeExporters(meter metric.Meter, exporters []exporter, files []fileExporter) {
	size, _ := meter.Int64ObservableUpDownCounter("hotline.exporter.queue.size", metric.WithUnit("{item}"),
		metric.WithDescription("Records or spans waiting to be sent to this exporter. "+
			"A queue that keeps growing will soon drop items."))
	up, _ := meter.Int64ObservableGauge("hotline.exporter.up",
		metric.WithDescription("1 while sends to this exporter work, 0 from the first failed send until one works again."))
	age, _ := meter.Float64ObservableGauge("hotline.exporter.queue.oldest.age", metric.WithUnit("s"),
		metric.WithDescription("How long the oldest item waiting for this exporter, in memory or on disk, has waited; 0 when none waits."))
	disk, _ := meter.Int64ObservableUpDownCounter("hotline.exporter.queue.disk.usage", metric.WithUnit("By"),
		metric.WithDescription("Bytes the disk queue's files of this exporter take, for exporters with a queueFolder."))
	observe := func(o metric.Observer, q *queue) {
		labels := metric.WithAttributeSet(q.labels)
		waiting, _ := q.fill()
		o.ObserveInt64(size, int64(waiting), labels)
		waited := 0.0
		if oldest := q.oldest(); !oldest.IsZero() {
			waited = time.Since(oldest).Seconds()
		}
		o.ObserveFloat64(age, waited, labels)
		if q.disk != nil {
			bytes, _ := q.disk.disk.waiting()
			o.ObserveInt64(disk, bytes, labels)
		}
	}
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, d := range exporters {
			observe(o, d.queue)
			observe(o, d.spans)
			o.ObserveInt64(up, d.up.Load(), metric.WithAttributeSet(d.place))
		}
		for _, f := range files {
			observe(o, f.queue)
			o.ObserveInt64(up, f.up.Load(), metric.WithAttributeSet(f.place))
		}
		return nil
	}, size, up, age, disk)
}

// exporterLabel names an exporter by its endpoint, as the config writes
// it.
func exporterLabel(e config.Exporter) attribute.KeyValue {
	return attribute.String("hotline.exporter", e.Endpoint)
}

// signalLabels names an exporter and what it sends: record or span.
func signalLabels(e config.Exporter, what string) attribute.Set {
	return attribute.NewSet(exporterLabel(e), attribute.String("hotline.signal", what))
}

// dropReason adds why items were dropped to an exporter's labels.
func dropReason(set attribute.Set, reason string) metric.MeasurementOption {
	return metric.WithAttributes(append(set.ToSlice(), attribute.String("hotline.drop.reason", reason))...)
}

// measured counts n sent or lost, and keeps the state of the exporter.
func measured(ctx context.Context, sent, dropped metric.Int64Counter, n int, set attribute.Set, err error, up *atomic.Int64) {
	if err != nil {
		up.Store(0)
		dropped.Add(ctx, int64(n), dropReason(set, "send_failed"))
		return
	}
	up.Store(1)
	sent.Add(ctx, int64(n), metric.WithAttributeSet(set))
}

// metricSender sends the proxy's own numbers to the telemetry section's
// collector over HTTP.
func metricSender(t *config.Telemetry, roots *x509.CertPool, timing Timing) sdkmetric.Exporter {
	temporality := sdkmetric.DefaultTemporalitySelector
	if t.Delta {
		temporality = delta
	}
	return httpMetrics(t.Exporter, tlsFor(t.Exporter, roots), timing, temporality)
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
