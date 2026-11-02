package proxy

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// own are the numbers the proxy keeps about itself, for the telemetry
// destination: calls in flight, how long calls take, failed TLS setups with
// apps, config reloads and certificates. No number is split by
// integration, vendor, host, URL or route, so they stay few however many
// integrations a proxy serves (Peter, 2026-10-31). Each has a description
// and a unit Grafana can chart; a 1 or 0 gauge has none, so its Prometheus
// name gets no _ratio ending (Peter, 2026-11-01).
type own struct {
	meter    metric.Meter
	active   metric.Int64UpDownCounter
	duration metric.Float64Histogram
	appTLS   metric.Int64Counter
	dropped  metric.Int64Counter
	// reloadFailed is 1 while the last reload failed, restart 1 while a
	// change waits for a restart.
	reloadFailed atomic.Int64
	restart      atomic.Int64
}

// newOwn makes the instruments. Their names, units and ranges are fixed
// and valid, so there is no error to handle.
func newOwn(meter metric.Meter) *own {
	o := &own{meter: meter}
	o.active, _ = meter.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"),
		metric.WithDescription("Calls from apps that the proxy has started and not yet finished. "+
			"A number that keeps growing means calls are stuck, often on a vendor that does not answer."))
	o.duration, _ = meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"),
		metric.WithDescription("How long each call took, as the app sees it: from when the call reached the proxy "+
			"until the last byte went back to the app. Vendor time included."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	o.appTLS, _ = meter.Int64Counter("hotline.app_tls.failures", metric.WithUnit("{connection}"),
		metric.WithDescription("App connections that failed to set up TLS with the proxy, before any call was made, "+
			"so no record exists for them. error.type says why, such as an app that does not trust the proxy's certificate."))
	o.dropped, _ = meter.Int64Counter("hotline.spec_check.dropped", metric.WithUnit("{check}"),
		metric.WithDescription("Spec checks skipped because the line of checks waiting for a worker was full: "+
			"the oldest waiting checks give way, and their call's record says hotline.schema.skipped=overloaded. "+
			"A growing number means the proxy needs more processor time or a larger specChecks.queueMemory."))
	return o
}

// checkDropped counts a spec check skipped as overloaded.
func (o *own) checkDropped() { o.dropped.Add(context.Background(), 1) }

// observe sends what the proxy is now: whether the last reload failed,
// whether a change waits for a restart, and when its certificates expire.
func (o *own) observe(p *Proxy) {
	failed, _ := o.meter.Int64ObservableGauge("hotline.config.reload_failed",
		metric.WithDescription("1 while the last reload of the config files failed and the proxy still runs "+
			"the config before it, else 0."))
	restart, _ := o.meter.Int64ObservableGauge("hotline.config.restart_needed",
		metric.WithDescription("1 while a changed setting, such as a listen address or an exporter, waits for "+
			"a restart to take effect, else 0."))
	notAfter, _ := o.meter.Int64ObservableGauge("hotline.certificate.not_after", metric.WithUnit("s"),
		metric.WithDescription("When a certificate of the proxy's own expires, in seconds since 1970: listen.tls is "+
			"the proxy port's certificate, listen.connect the issuer for CONNECT tunnels. "+
			"Once it expires, apps stop trusting the proxy."))
	memory, _ := o.meter.Int64ObservableUpDownCounter("hotline.spec_check.queue.memory.usage", metric.WithUnit("By"),
		metric.WithDescription("Memory the bodies of spec checks waiting for a worker hold now. "+
			"It stays at most specChecks.queueMemory: beyond it, the oldest waiting checks are skipped."))
	waiting, _ := o.meter.Int64ObservableUpDownCounter("hotline.spec_check.queue.size", metric.WithUnit("{check}"),
		metric.WithDescription("Spec checks waiting for a worker now. A number that stays high means the checks "+
			"cannot keep up with the calls."))
	_, _ = o.meter.RegisterCallback(func(_ context.Context, ob metric.Observer) error {
		ob.ObserveInt64(failed, o.reloadFailed.Load())
		ob.ObserveInt64(restart, o.restart.Load())
		if p.appCert != nil {
			ob.ObserveInt64(notAfter, p.appCert.get().Leaf.NotAfter.Unix(),
				metric.WithAttributes(attribute.String("hotline.certificate.use", "listen.tls")))
		}
		if p.handler.intercept != nil {
			ob.ObserveInt64(notAfter, p.handler.intercept.issuer.get().Leaf.NotAfter.Unix(),
				metric.WithAttributes(attribute.String("hotline.certificate.use", "listen.connect")))
		}
		held, size := p.handler.checks.held()
		ob.ObserveInt64(memory, held)
		ob.ObserveInt64(waiting, int64(size))
		return nil
	}, failed, restart, notAfter, memory, waiting)
}

// started counts a call in flight, until the returned func is called.
func (o *own) started(ingress string) func() {
	labels := metric.WithAttributes(attribute.String("hotline.ingress", ingress))
	o.active.Add(context.Background(), 1, labels)
	return func() { o.active.Add(context.Background(), -1, labels) }
}

// ended measures a finished call's total time from its record, by ingress,
// status code and error type.
func (o *own) ended(attrs []attribute.KeyValue) {
	var labels []attribute.KeyValue
	var total int64
	for _, kv := range attrs {
		switch kv.Key {
		case semconv.HTTPResponseStatusCodeKey, semconv.ErrorTypeKey, "hotline.ingress":
			labels = append(labels, kv)
		case "hotline.timing.total_us":
			total = kv.Value.AsInt64()
		}
	}
	o.duration.Record(context.Background(), float64(total)/1e6, metric.WithAttributes(labels...))
}

// tlsFailed counts an app connection whose TLS setup failed, inside a
// tunnel or, with no ingress, on the TLS port.
func (o *own) tlsFailed(err error, labels ...attribute.KeyValue) {
	o.appTLS.Add(context.Background(), 1,
		metric.WithAttributes(append(labels, semconv.ErrorTypeKey.String(tlsFailure(err)))...))
}

// reloaded keeps whether the last reload failed, and whether a setting a
// good one changed waits for a restart.
func (o *own) reloaded(ok, restart bool) {
	if !ok {
		o.reloadFailed.Store(1)
		return
	}
	o.reloadFailed.Store(0)
	o.restart.Store(0)
	if restart {
		o.restart.Store(1)
	}
}

// tlsFailure says why an app's TLS setup failed: plain HTTP on the TLS
// port, only TLS versions the proxy does not take, or the app not trusting
// the certificate it was shown.
func tlsFailure(err error) string {
	switch {
	case errors.Is(err, errPlainHTTP):
		return "plain_http"
	case strings.Contains(err.Error(), "unsupported versions"):
		return "tls_version"
	}
	return untrustedOr(err)
}

// untrustedOr is tls_untrusted_by_app when the app refused the certificate
// it was shown, else tls_handshake_failed.
func untrustedOr(err error) string {
	if text := err.Error(); strings.Contains(text, "remote error: tls: ") && strings.Contains(text, "certificate") {
		return "tls_untrusted_by_app"
	}
	return "tls_handshake_failed"
}
