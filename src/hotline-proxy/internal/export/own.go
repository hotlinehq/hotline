package export

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"slices"
	"sync/atomic"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"hotline-proxy/internal/config"
)

// Own sends the numbers the proxy keeps about itself to the telemetry
// destination, and nowhere else. Without one they are not measured at all.
type Own struct {
	provider *sdkmetric.MeterProvider
	meter    metric.Meter
}

// NewOwn starts the pipeline of the proxy's own numbers, with the Go
// runtime's among them. A metric telemetry.disable names is dropped before
// it is measured. With no telemetry destination, nothing is measured.
func NewOwn(t *config.Telemetry, roots *x509.CertPool, service Service, timing Timing) *Own {
	if t == nil {
		return &Own{meter: noop.NewMeterProvider().Meter("hotline-proxy")}
	}
	options := []sdkmetric.Option{
		sdkmetric.WithResource(describe(service, t.Exporter.Resource)),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricSender(t.Exporter, roots, timing),
			sdkmetric.WithInterval(t.Exporter.MetricInterval),
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

// destinationTLS trusts roots and the destination's own issuers, and shows
// its client certificate, if any.
func destinationTLS(e config.Exporter, roots *x509.CertPool) *tls.Config {
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

// observeDestinations sends, for every exporter, how many items wait in
// its queues and whether its last send worked.
func observeDestinations(meter metric.Meter, destinations []destination) {
	size, _ := meter.Int64ObservableUpDownCounter("hotline.exporter.queue.size", metric.WithUnit("{item}"),
		metric.WithDescription("Records or spans waiting to be sent to this exporter. "+
			"A queue that keeps growing will soon drop items."))
	up, _ := meter.Int64ObservableGauge("hotline.exporter.up",
		metric.WithDescription("1 while sends to this exporter work, 0 from the first failed send until one works again."))
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, d := range destinations {
			waiting, _ := d.queue.fill()
			o.ObserveInt64(size, int64(waiting), metric.WithAttributeSet(d.queue.labels))
			waiting, _ = d.spans.fill()
			o.ObserveInt64(size, int64(waiting), metric.WithAttributeSet(d.spans.labels))
			o.ObserveInt64(up, d.up.Load(), metric.WithAttributeSet(d.place))
		}
		return nil
	}, size, up)
}

// exporterLabel names an exporter by its endpoint, as the config writes
// it.
func exporterLabel(e config.Exporter) attribute.KeyValue {
	return attribute.String("hotline.exporter", e.Endpoint)
}

// signal names an exporter and what it sends: record, span or count.
func signal(e config.Exporter, what string) attribute.Set {
	return attribute.NewSet(exporterLabel(e), attribute.String("hotline.signal", what))
}

// dropReason adds why items were dropped to an exporter's labels.
func dropReason(set attribute.Set, reason string) metric.MeasurementOption {
	return metric.WithAttributes(append(set.ToSlice(), attribute.String("hotline.drop.reason", reason))...)
}

// countedMetrics counts the data points each send of hotline.requests
// carried, and keeps the exporter's state.
type countedMetrics struct {
	sdkmetric.Exporter
	sent    metric.Int64Counter
	dropped metric.Int64Counter
	labels  attribute.Set
	up      *atomic.Int64
}

func (c countedMetrics) Export(ctx context.Context, rm *metricdata.ResourceMetrics) error {
	err := c.Exporter.Export(ctx, rm)
	n := 0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			// Destinations get only the hotline.requests sum.
			sum, _ := m.Data.(metricdata.Sum[int64])
			n += len(sum.DataPoints)
		}
	}
	measured(ctx, c.sent, c.dropped, n, c.labels, err, c.up)
	return err
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
