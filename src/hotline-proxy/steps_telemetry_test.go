package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func telemetrySteps(sc *godog.ScenarioContext, sut *telemetrySUT) {
	sc.Step(`^the proxy runs$`, func() error {
		if sut.receiver == nil {
			sut.receiver = newReceiver()
		}
		return sut.app.defaultCall()
	})
	sc.Step(`^a fake OTLP receiver for the proxy's own telemetry$`, func() error {
		sut.receiver = newReceiver()
		return nil
	})
	sc.Step(`^the telemetry section does not set metricInterval$`, func() error {
		sut.unsetOwnInterval = true
		return nil
	})
	sc.Step(`^the telemetry section sets (logInterval|traceInterval)$`, func(name string) error {
		sut.telemetryExtra = "    " + name + ": 5s\n"
		return nil
	})
	sc.Step(`^the telemetry receiver gets no metric within a second$`, func() error {
		pause(time.Second)
		if points := sut.receiver.metricPoints(); len(points) > 0 {
			return failf("got %d data points within a second, the first %s", len(points), points[0].name)
		}
		return nil
	})
	sc.Step(`^the telemetry receiver gets the metric (\S+) once the proxy stops$`, func(name string) error {
		sut.proxy.settle()
		for _, p := range sut.receiver.metricPoints() {
			if p.name == name {
				return nil
			}
		}
		return failf("the telemetry receiver got no %s", name)
	})
	sc.Step(`^a config with no telemetry section$`, func() error {
		sut.receiver.stop()
		sut.receiver = nil
		return nil
	})
	sc.Step(`^the telemetry section turns on the profile pages$`, func() error {
		sut.telemetryPprof = true
		return nil
	})
	sc.Step(`^the telemetry section turns off (\S+)$`, func(name string) error {
		sut.telemetryDisable = append(sut.telemetryDisable, name)
		return nil
	})

	sc.Step(`^the telemetry receiver gets the (?:metric|histogram) (\S+)$`, func(name string) error {
		_, err := sut.ownPoints(name)
		return err
	})
	sc.Step(`^the telemetry receiver gets the metrics (\S+) and (\S+)$`, func(a, b string) error {
		if _, err := sut.ownPoints(a); err != nil {
			return err
		}
		_, err := sut.ownPoints(b)
		return err
	})
	sc.Step(`^the telemetry receiver gets the histogram (\S+) with unit (\S+)$`, func(name, unit string) error {
		points, err := sut.ownPoints(name)
		if err != nil {
			return err
		}
		if points[0].kind != "histogram" || points[0].unit != unit {
			return failf("%s is a %s with unit %q", name, points[0].kind, points[0].unit)
		}
		return nil
	})
	sc.Step(`^the telemetry receiver gets (\S+) with unit "([^"]*)" and a description$`, func(name, unit string) error {
		points, err := sut.ownPoints(name)
		if err != nil {
			return err
		}
		if points[0].unit != unit || points[0].description == "" {
			return failf("%s has the unit %q and the description %q", name, points[0].unit, points[0].description)
		}
		return nil
	})
	sc.Step(`^the telemetry receiver gets no log records$`, func() error {
		sut.proxy.settle()
		if records := sut.receiver.got(); len(records) > 0 {
			return failf("the telemetry receiver got %d records", len(records))
		}
		return nil
	})
	sc.Step(`^the product receiver gets no metric$`, func() error {
		sut.proxy.settle()
		if points := sut.otlp.first().metricPoints(); len(points) > 0 {
			return failf("the product receiver got %s", points[0].name)
		}
		return nil
	})
	sc.Step(`^the telemetry section (does not set temporality|sets temporality to delta)$`, func(sets string) error {
		if sets == "sets temporality to delta" {
			sut.telemetryExtra = "    temporality: delta\n"
		}
		return nil
	})
	sc.Step(`^the telemetry receiver is plain and offers h2c$`, func() error {
		sut.receiver.stop()
		sut.receiver = newReceiverWith(receiverSetup{h2c: true})
		return nil
	})
	sc.Step(`^the telemetry section sets h2c to true$`, func() error {
		sut.telemetryExtra = "    h2c: true\n"
		return nil
	})
	sc.Step(`^every request to the telemetry receiver came over HTTP/2.0$`, func() error {
		sut.proxy.settle()
		seen := sut.receiver.requests()
		if len(seen) == 0 {
			return failf("the telemetry receiver got no request")
		}
		for _, s := range seen {
			if s.proto != "HTTP/2.0" {
				return failf("a request to %s came over %s, want HTTP/2.0", s.path, s.proto)
			}
		}
		return nil
	})
	sc.Step(`^the telemetry sums are (cumulative|delta)$`, func(kind string) error {
		sut.proxy.settle()
		want := map[string]metrics.AggregationTemporality{
			"cumulative": metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			"delta":      metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		}[kind]
		sums := 0
		for _, p := range sut.receiver.metricPoints() {
			if p.kind != "sum" {
				continue
			}
			sums++
			if p.temporality != want {
				return failf("%s has %v totals, want %s", p.name, p.temporality, kind)
			}
		}
		if sums == 0 {
			return failf("the telemetry receiver got no sum")
		}
		return nil
	})
	sc.Step(`^the telemetry receiver gets no metric (\S+)$`, func(name string) error {
		return sut.noOwnMetric(func(got string) bool { return got == name })
	})
	sc.Step(`^the telemetry receiver gets no metric starting with (\S+)$`, func(prefix string) error {
		return sut.noOwnMetric(func(got string) bool { return strings.HasPrefix(got, prefix) })
	})

	sc.Step(`^a vendor that keeps the call waiting$`, func() error {
		sut.app.freed = make(chan struct{})
		freed := sut.app.freed
		sut.vendors.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			<-freed
			rw.WriteHeader(http.StatusOK)
		})
		return nil
	})
	sc.Step(`^an app starts a forward proxy GET call over http$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		sut.app.callDone = done
		go func() { done <- sut.app.call(http.MethodGet, "http://api.talon.one/v2/customer_sessions/sess-8812") }()
		return sut.vendors.waitForVendorCalls("api.talon.one", 1)
	})
	sc.Step(`^once the call ends it is 0$`, func() error {
		close(sut.app.freed)
		if err := <-sut.app.callDone; err != nil {
			return err
		}
		return sut.ownValue("http.server.active_requests", "hotline.ingress forward_proxy", 0)
	})
	sc.Step(`^an app makes a forward proxy GET call over http(?: and the vendor answers 200)?$`, func() error {
		return sut.app.call(http.MethodGet, "http://api.talon.one/v2/customer_sessions/sess-8812")
	})
	sc.Step(`^the vendor's name does not resolve$`, func() error {
		sut.vendors.missing["api.talon.one"] = true
		return nil
	})
	sc.Step(`^an app makes a forward proxy call with the method (\S+)$`, func(method string) error {
		return sut.app.call(method, "http://api.talon.one/v2/customer_sessions/sess-8812")
	})

	sc.Step(`^(\S+) (?:is|turns) (\d+) for (.+)$`, func(name string, want int, labels string) error {
		if labels == "that exporter" {
			labels = sut.exporterLabels()
		}
		sut.lastMetric = name
		return sut.ownValue(name, labels, float64(want))
	})
	sc.Step(`^(\S+) counts (\d+) calls? (?:for|with) (.+)$`, func(name string, want uint64, labels string) error {
		if name == "it" {
			name = sut.lastMetric
		}
		return sut.ownCount(name, labels, want)
	})
	sc.Step(`^its range limits are (.+)$`, func(list string) error {
		points, err := sut.ownPoints(sut.lastMetric)
		if err != nil {
			return err
		}
		var want []float64
		for _, item := range splitList(list) {
			f, _ := strconv.ParseFloat(item, 64)
			want = append(want, f)
		}
		if !slices.Equal(points[0].bounds, want) {
			return failf("%s has the range limits %v, want %v", sut.lastMetric, points[0].bounds, want)
		}
		return nil
	})
	sc.Step(`^it has no label but (.+)$`, func(list string) error {
		allowed := splitList(list)
		points, err := sut.ownPoints(sut.lastMetric)
		if err != nil {
			return err
		}
		for _, p := range points {
			for key := range p.attrs {
				if !slices.Contains(allowed, key) {
					return failf("%s has the label %s", sut.lastMetric, key)
				}
			}
		}
		return nil
	})
	sc.Step(`^(\S+) has no numbers for (.+)$`, func(name, labels string) error {
		points, err := sut.ownPoints(name)
		if err != nil {
			return err
		}
		for _, p := range points {
			if matches(p, parseLabels(labels)) {
				return failf("%s has numbers for %s", name, labels)
			}
		}
		return nil
	})

	sc.Step(`^(\S+) is sent for that exporter and hotline.signal (\S+)$`, func(name, signal string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.proxy.eventually(func() error {
			if _, ok := sut.latest(name, parseLabels(sut.exporterLabels()+" and hotline.signal "+signal)); !ok {
				return failf("no %s for that exporter and %s; got %v", name, signal, sut.lastPoints(name))
			}
			return nil
		})
	})
	sc.Step(`^(\S+) counts (\d+|some) for that exporter(?:,| and) (.+)$`, func(name, want, labels string) error {
		labels = strings.Replace(labels, " and ", ", ", 1)
		labels = sut.exporterLabels() + ", " + labels
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.proxy.eventually(func() error {
			p, ok := sut.latest(name, parseLabels(labels))
			n, _ := strconv.Atoi(want)
			if !ok || want == "some" && p.value <= 0 || want != "some" && p.value != float64(n) {
				return failf("%s is %v for %q, want %s; got %v", name, p.value, labels, want, sut.lastPoints(name))
			}
			return nil
		})
	})
	sc.Step(`^3 calls end$`, func() error {
		for range 3 {
			if err := sut.app.defaultCall(); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^a product exporter over HTTP that refuses every log export with 400$`, func() error {
		sut.otlp.first().stop()
		sut.otlp.replaceFirst(newReceiverWith(receiverSetup{}))
		for range 100 {
			sut.otlp.first().answerNextLogExports(answer{status: http.StatusBadRequest})
		}
		return nil
	})
	sc.Step(`^a product exporter that is down$`, func() error {
		sut.otlp.first().stop()
		// Nothing listens there yet.
		sut.otlp.replaceFirst(&receiver{endpoint: "http://" + sut.proxy.ports.take()})
		return nil
	})
	sc.Step(`^the exporter comes back and another call ends$`, func() error {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", strings.TrimPrefix(sut.otlp.first().endpoint, "http://"))
		if err != nil {
			return err
		}
		sut.otlp.replaceFirst(newReceiverWith(receiverSetup{listener: l}))
		return sut.app.defaultCall()
	})

	sc.Step(`^(hotline\.config\.\S+) (?:is|turns) (\d+)$`, func(name string, want int) error {
		return sut.ownValue(name, "", float64(want))
	})
	sc.Step(`^the config file is put back as it was$`, func() error {
		sut.proxy.writeFile("hotline-proxy.yaml", sut.proxy.configText())
		return nil
	})

	sc.Step(`^the proxy port takes TLS(?: with a certificate that expires in (\d+) days)?$`, func(days string) error {
		n, _ := strconv.Atoi(days)
		sut.tlsIngress.useCertificateValidFor(time.Duration(max(n, 90)) * 24 * time.Hour)
		sut.tlsIngress.useAppCertificate()
		sut.app.appTLS = sut.tlsIngress.appTLSConfig()
		return nil
	})
	sc.Step(`^the cert and key files are replaced with a certificate that expires in (\d+) days$`, func(days int) error {
		sut.tlsIngress.renewCertificateValidFor(time.Duration(days) * 24 * time.Hour)
		return nil
	})
	sc.Step(`^hotline.certificate.not_after is that certificate's expiry for hotline.certificate.use proxy.tls$`, func() error {
		return sut.ownValue("hotline.certificate.not_after", "hotline.certificate.use proxy.tls", expiry(sut.tlsIngress.certificate()))
	})
	sc.Step(`^hotline.certificate.not_after turns to the new certificate's expiry for hotline.certificate.use proxy.tls$`, func() error {
		return sut.ownValue("hotline.certificate.not_after", "hotline.certificate.use proxy.tls", expiry(sut.tlsIngress.renewedCertificate()))
	})
	sc.Step(`^CONNECT is an ingress with a certificate authority that expires in (\d+) days$`, func(days int) error {
		sut.connect.useInterceptionAuthority(newAuthorityValid(time.Now().Add(-time.Hour), time.Now().Add(time.Duration(days)*24*time.Hour)))
		return nil
	})
	sc.Step(`^hotline.certificate.not_after is that certificate authority's expiry for hotline.certificate.use proxy.connect$`, func() error {
		return sut.ownValue("hotline.certificate.not_after", "hotline.certificate.use proxy.connect", float64(sut.certs.interceptionAuthority().cert.NotAfter.Unix()))
	})

	sc.Step(`^an app that does not trust the proxy opens TLS$`, func() error {
		sut.app.appTLS = &tls.Config{RootCAs: x509.NewCertPool(), ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
		return sut.tryTLS()
	})
	sc.Step(`^an app that allows only TLS 1.1 opens TLS$`, func() error {
		sut.app.appTLS.MinVersion, sut.app.appTLS.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
		return sut.tryTLS()
	})
	sc.Step(`^an app sends plain HTTP to the TLS port$`, func() error {
		sut.app.appTLS = nil
		return sut.app.defaultCall()
	})
	sc.Step(`^an app that does not trust the certificate authority opens a tunnel$`, func() error {
		sut.connect.useInterceptionAuthority(newAuthority())
		sut.app.tunnelRoots = x509.NewCertPool()
		return sut.connect.openTunnel("api.talon.one", "443", "talon-shop-a")
	})
	sc.Step(`^hotline.app_tls.failures counts (\d+) with error.type (\S+) and hotline.ingress (\S+)$`, func(want int, errorType, ingress string) error {
		labels := "error.type " + errorType
		if ingress != "none" {
			labels += " and hotline.ingress " + ingress
		}
		return sut.proxy.eventually(func() error {
			p, ok := sut.latest("hotline.app_tls.failures", parseLabels(labels))
			_, hasIngress := p.attrs["hotline.ingress"]
			if !ok || p.value != float64(want) || hasIngress != (ingress != "none") {
				return failf("hotline.app_tls.failures is %v for %s", p.value, labels)
			}
			return nil
		})
	})

	sc.Step(`^the proxy logs a warning naming (\S+)$`, func(name string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.proxy.warned(name)
	})
	sc.Step(`^the proxy logs a warning that its own telemetry is not sent$`, func() error {
		return sut.proxy.warned("telemetry is not sent")
	})
}

// ownPoints waits until the telemetry receiver has data points of a metric,
// and returns the latest of each set of labels.
func (sut *telemetrySUT) ownPoints(name string) ([]receivedPoint, error) {
	sut.lastMetric = name
	if err := sut.proxy.start(); err != nil {
		return nil, err
	}
	var points []receivedPoint
	err := sut.proxy.eventually(func() error {
		points = sut.lastPoints(name)
		if len(points) == 0 {
			return failf("the telemetry receiver got no %s", name)
		}
		return nil
	})
	return points, err
}

// lastPoints are the latest data points of a metric, one for each set of
// labels, as the telemetry receiver got them.
func (sut *telemetrySUT) lastPoints(name string) []receivedPoint {
	latest := map[string]receivedPoint{}
	var order []string
	for _, p := range sut.receiver.metricPoints() {
		if p.name != name {
			continue
		}
		key := fmt.Sprint(p.attrs)
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = p
	}
	var points []receivedPoint
	for _, key := range order {
		points = append(points, latest[key])
	}
	return points
}

// latest is the latest data point of a metric with every label given.
func (sut *telemetrySUT) latest(name string, labels map[string]string) (receivedPoint, bool) {
	for _, p := range sut.lastPoints(name) {
		if matches(p, labels) {
			return p, true
		}
	}
	return receivedPoint{}, false
}

// ownValue waits until a metric's latest value with the labels is want.
func (sut *telemetrySUT) ownValue(name, labels string, want float64) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	return sut.proxy.eventually(func() error {
		p, ok := sut.latest(name, parseLabels(labels))
		if !ok || p.value != want {
			return failf("%s is %v for %q, want %v; got %v", name, p.value, labels, want, sut.lastPoints(name))
		}
		return nil
	})
}

// ownCount waits until a histogram has counted want calls with the labels.
func (sut *telemetrySUT) ownCount(name, labels string, want uint64) error {
	sut.lastMetric = name
	return sut.proxy.eventually(func() error {
		p, ok := sut.latest(name, parseLabels(labels))
		if !ok || p.count != want {
			return failf("%s counts %d for %q, want %d; got %v", name, p.count, labels, want, sut.lastPoints(name))
		}
		return nil
	})
}

// noOwnMetric checks that no metric the test names arrived, once the
// proxy's own numbers have been sent at least once.
func (sut *telemetrySUT) noOwnMetric(unwanted func(string) bool) error {
	if _, err := sut.ownPoints("hotline.config.restart_needed"); err != nil {
		return err
	}
	for _, p := range sut.receiver.metricPoints() {
		if unwanted(p.name) {
			return failf("the telemetry receiver got %s", p.name)
		}
	}
	return nil
}

// eventually tries a check until it passes, for up to 10 seconds.
func (sut *proxySUT) eventually(check func() error) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := check()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		pause(10 * time.Millisecond)
	}
}

// exporterLabels name the first product exporter, by its endpoint as the
// config writes it.
func (sut *telemetrySUT) exporterLabels() string {
	return "hotline.exporter " + sut.otlp.first().endpoint
}

// tryTLS opens TLS to the proxy, which may fail.
func (sut *telemetrySUT) tryTLS() error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	conn, err := sut.app.dialProxy()
	if err == nil {
		_ = conn.Close()
	}
	return nil
}

func expiry(cert tls.Certificate) float64 {
	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	return float64(parsed.NotAfter.Unix())
}

// splitList splits "a, b and c" into its items.
func splitList(list string) []string {
	return strings.FieldsFunc(strings.ReplaceAll(list, " and ", ", "), func(r rune) bool { return r == ',' || r == ' ' })
}

// parseLabels reads "name value, name value and name value".
func parseLabels(labels string) map[string]string {
	parsed := map[string]string{}
	for _, pair := range strings.Split(strings.ReplaceAll(labels, " and ", ", "), ", ") {
		if name, value, ok := strings.Cut(strings.TrimSpace(pair), " "); ok {
			parsed[name] = value
		}
	}
	return parsed
}

// matches says whether a data point has every label given.
func matches(p receivedPoint, labels map[string]string) bool {
	for name, value := range labels {
		got, ok := p.attrs[name]
		if !ok || fmt.Sprint(got) != value {
			return false
		}
	}
	return true
}

// telemetrySUT is the proxy's own health data: its metrics, the receiver
// they go to, and the profile pages.
type telemetrySUT struct {
	proxy      *proxySUT
	vendors    *vendorsSUT
	certs      *certsSUT
	app        *appSUT
	otlp       *otlpSUT
	tlsIngress *tlsIngressSUT
	connect    *connectSUT
	// receiver, when set, is where the proxy's own health data goes.
	receiver *receiver
	// telemetryDisable are the metrics the telemetry section turns off.
	telemetryDisable []string
	// lastMetric is the proxy's own metric a scenario last looked at.
	lastMetric string
	// telemetryExtra are more lines for the telemetry section's otlp.
	telemetryExtra string
	// telemetryPprof turns on the profile pages in the telemetry section.
	telemetryPprof bool
	// unsetOwnInterval leaves the interval out of the telemetry section, so
	// the default applies. Otherwise it is 20ms, to keep tests quick.
	unsetOwnInterval bool
}

func newTelemetrySUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, otlp *otlpSUT, tlsIngress *tlsIngressSUT, connect *connectSUT) *telemetrySUT {
	sut := &telemetrySUT{proxy: root, vendors: vendors, certs: certs, app: app, otlp: otlp, tlsIngress: tlsIngress, connect: connect}
	root.addConfig(configTelemetry, sut.section)
	return sut
}

// AddSteps adds the steps of the proxy's own telemetry.
func (sut *telemetrySUT) AddSteps(sc *godog.ScenarioContext) {
	telemetrySteps(sc, sut)
}

// Close stops what the proxy's own telemetry left running.
func (sut *telemetrySUT) Close() {
	if sut.receiver != nil {
		sut.receiver.stop()
	}
}

// section is the config's telemetry section.
func (sut *telemetrySUT) section() string {
	if sut.receiver == nil {
		if sut.telemetryPprof {
			return "telemetry:\n  pprof: true\n"
		}
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "telemetry:\n  otlp:\n    endpoint: %s\n", sut.receiver.endpoint)
	if !sut.unsetOwnInterval {
		b.WriteString("    metricInterval: 20ms\n")
	}
	b.WriteString(sut.telemetryExtra)
	for i, name := range sut.telemetryDisable {
		if i == 0 {
			b.WriteString("  disable:\n")
		}
		fmt.Fprintf(&b, "    - %q\n", name)
	}
	if sut.telemetryPprof {
		b.WriteString("  pprof: true\n")
	}
	return b.String()
}

// turnOffMetrics turns off a metric in the telemetry section, and
// turnOnProfilePages turns on the profile pages.
func (sut *telemetrySUT) turnOffMetrics(metric string) {
	sut.telemetryDisable = append(sut.telemetryDisable, metric)
}
func (sut *telemetrySUT) turnOnProfilePages() { sut.telemetryPprof = true }

// metricPoints are the proxy's own metric points the receiver got.
func (sut *telemetrySUT) metricPoints() []receivedPoint { return sut.receiver.metricPoints() }
