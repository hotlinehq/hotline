package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func exportSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a fake OTLP receiver$`, func() error { return nil })
	sc.Step(`^the proxy sends any signal$`, func() error { return w.defaultCall() })
	sc.Step(`^its resource has service.name hotline-proxy, service.version and service.instance.id$`, func() error {
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		res := records[0].resource
		if err := has(res, "service.name", "hotline-proxy"); err != nil {
			return err
		}
		if err := has(res, "service.version", "1.0.0-test"); err != nil {
			return err
		}
		return has(res, "service.instance.id", "proxy-test-1")
	})
	sc.Step(`^an exporter with the resource service.name invoices-proxy, service.instance.id from the env POD_NAME and deployment.environment.name from the file env-name$`, func() error {
		w.env = map[string]string{"POD_NAME": "invoices-proxy-7d9f"}
		file := w.writeFile("env-name", "production\n")
		w.exporterExtra = []string{"      resource:\n" +
			"        service.name: invoices-proxy\n" +
			"        service.instance.id: { env: POD_NAME }\n" +
			"        deployment.environment.name: { file: " + file + " }\n"}
		return nil
	})
	sc.Step(`^its resource has service.name invoices-proxy, service.instance.id from POD_NAME and deployment.environment.name from the file$`, func() error {
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		res := records[0].resource
		if err := has(res, "service.name", "invoices-proxy"); err != nil {
			return err
		}
		if err := has(res, "service.instance.id", "invoices-proxy-7d9f"); err != nil {
			return err
		}
		return has(res, "deployment.environment.name", "production")
	})
	sc.Step(`^service.version is still the proxy's own$`, func() error {
		return has(w.records()[0].resource, "service.version", "1.0.0-test")
	})
	sc.Step(`^the hotline.requests counter carries the same resource$`, func() error {
		counts := w.counts()
		if len(counts) == 0 {
			return failf("no count arrived")
		}
		if fmt.Sprint(counts[0].resource) != fmt.Sprint(w.records()[0].resource) {
			return failf("the counter's resource is %v, the record's %v", counts[0].resource, w.records()[0].resource)
		}
		return nil
	})
	sc.Step(`^two exporters, the second with the resource deployment.environment.name staging$`, func() error {
		w.receivers = append(w.receivers, newReceiver())
		w.exporterExtra = []string{"", "      resource:\n        deployment.environment.name: staging\n"}
		return nil
	})
	sc.Step(`^the first gets the proxy's own resource, with no deployment.environment.name$`, func() error {
		w.settle()
		records, _ := w.receivers[0].got()
		if len(records) != 1 {
			return failf("the first exporter got %d records", len(records))
		}
		if _, set := records[0].resource["deployment.environment.name"]; set {
			return failf("the first exporter's resource is %v", records[0].resource)
		}
		return has(records[0].resource, "service.name", "hotline-proxy")
	})
	sc.Step(`^the second gets deployment.environment.name staging$`, func() error {
		records, _ := w.receivers[1].got()
		if len(records) != 1 {
			return failf("the second exporter got %d records", len(records))
		}
		if err := has(records[0].resource, "deployment.environment.name", "staging"); err != nil {
			return err
		}
		return has(records[0].resource, "service.name", "hotline-proxy")
	})
	sc.Step(`^the record's level is (INFO|WARN|ERROR)$`, func(level string) error {
		records := w.records()
		if len(records) == 0 {
			return failf("no record arrived; the app got %v %q", w.resp, w.respBody)
		}
		last := records[len(records)-1]
		numbers := map[string]int32{"INFO": 9, "WARN": 13, "ERROR": 17}
		if last.severityText != level || last.severityNumber != numbers[level] {
			return failf("the level is %q (%d), want %s (%d)", last.severityText, last.severityNumber, level, numbers[level])
		}
		return nil
	})
	sc.Step(`^the receiver gets one OTLP log record with all of the record's attributes$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasAll(record, "hotline.integration.id", "hotline.ingress", "url.full", "url.template",
			"http.response.status_code", "hotline.timing.total_us", "hotline.config.version")
	})
	sc.Step(`^standard names come from the semconv constants, such as http.request.method and url.full$`, func() error {
		return w.lastHasAll("http.request.method", "url.full", "url.scheme", "client.address", "server.address", "server.port",
			"network.peer.address", "network.protocol.version", "http.request.body.size", "http.response.body.size",
			"tls.protocol.version", "tls.server.not_after")
	})
	sc.Step(`^every other field has the exact hotline. name from the spec$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		standard := []string{"http.request.method", "url.full", "url.scheme", "url.template", "client.address", "server.address",
			"server.port", "network.peer.address", "network.protocol.version", "http.response.status_code",
			"error.type", "http.request.body.size", "http.response.body.size", "tls.protocol.version",
			"tls.server.not_after"}
		hotline := []string{"hotline.integration.id", "hotline.ingress", "hotline.timing.total_us",
			"hotline.client.cancelled", "hotline.config.version", "hotline.trace.signature", "hotline.schema.request.errors",
			"hotline.schema.response.errors", "hotline.schema.request.errors_total", "hotline.schema.response.errors_total",
			"hotline.schema.skipped", "hotline.schema.status", "hotline.signature", "hotline.signature_key_id",
			"hotline.request.body.signature", "hotline.response.body.signature"}
		known := map[string]bool{}
		for _, k := range append(standard, hotline...) {
			known[k] = true
		}
		for key := range record {
			if !known[key] && !strings.HasPrefix(key, "http.request.header.") && !strings.HasPrefix(key, "http.response.header.") {
				return failf("the record has %s, which is not in the spec", key)
			}
		}
		return nil
	})
	sc.Step(`^numbers are numbers, true and false are booleans and header values are lists of strings$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		checks := map[string]string{
			"http.response.status_code": "int64", "server.port": "int64", "hotline.timing.total_us": "int64",
			"hotline.client.cancelled":         "bool",
			"http.request.header.content-type": "[]interface {}", "url.full": "string",
		}
		for key, kind := range checks {
			if got := fmt.Sprintf("%T", record[key]); got != kind {
				return failf("%s is a %s, want %s", key, got, kind)
			}
		}
		return nil
	})

	sc.Step(`^hotline.requests goes up by one, with unit \{request\}$`, func() error {
		counts := w.counts()
		if len(counts) == 0 {
			return failf("no count arrived")
		}
		last := counts[len(counts)-1]
		if last.value != 1 || last.unit != "{request}" || !last.monotonic {
			return failf("got %+v, want 1 {request} that only goes up", last)
		}
		return nil
	})
	sc.Step(`^it carries hotline.integration.id, url.template, http.request.method, http.response.status_code and error.type$`, func() error {
		counts := w.counts()
		attrs := counts[len(counts)-1].attrs
		if err := hasAll(attrs, "hotline.integration.id", "url.template", "http.request.method", "http.response.status_code"); err != nil {
			return err
		}
		for key := range attrs {
			switch key {
			case "hotline.integration.id", "url.template", "http.request.method", "http.response.status_code", "error.type":
			default:
				return failf("the counter carries %s", key)
			}
		}
		return nil
	})
	sc.Step(`^a not-passed call ends$`, func() error {
		w.appID = ""
		return w.defaultCall()
	})
	sc.Step(`^hotline.requests carries no (\S+)$`, func(name string) error {
		counts := w.counts()
		if len(counts) == 0 {
			return failf("no count arrived")
		}
		return hasNo(counts[len(counts)-1].attrs, name)
	})

	sc.Step(`^an exporter that (does not set temporality|sets temporality to delta)$`, func(sets string) error {
		if sets == "sets temporality to delta" {
			w.exporterExtra = []string{"      temporality: delta\n"}
		}
		return nil
	})
	sc.Step(`^an exporter that sets no intervals$`, func() error {
		w.unsetIntervals = true
		return nil
	})
	sc.Step(`^an exporter that sets (metricInterval|logInterval|traceInterval) to 1h$`, func(name string) error {
		w.exporterExtra = []string{"      " + name + ": 1h\n"}
		return nil
	})
	sc.Step(`^nothing arrives within a second$`, func() error {
		time.Sleep(time.Second)
		if got := w.arrived(); len(got) > 0 {
			return failf("got %v within a second", got)
		}
		return nil
	})
	sc.Step(`^the record, its spans and the count arrive when the proxy stops$`, func() error {
		w.settle()
		if got := w.arrived(); len(got) != 3 {
			return failf("got only %v", got)
		}
		return nil
	})
	sc.Step(`^no (count|record|span) arrives within a second, but the others do$`, func(signal string) error {
		err := w.eventually(func() error {
			if got := w.arrived(); len(got) < 2 {
				return failf("got only %v", got)
			}
			return nil
		})
		if err != nil {
			return err
		}
		time.Sleep(time.Second)
		if got := w.arrived(); slices.Contains(got, signal) {
			return failf("the %s arrived within a second", signal)
		}
		return nil
	})
	sc.Step(`^the (count|record|span) arrives when the proxy stops$`, func(signal string) error {
		w.settle()
		if got := w.arrived(); !slices.Contains(got, signal) {
			return failf("no %s arrived, got %v", signal, got)
		}
		return nil
	})
	sc.Step(`^calls end$`, func() error {
		for range 2 {
			if err := w.defaultCall(); err != nil {
				return err
			}
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	sc.Step(`^the (?:totals are|exporter's temporality is) (cumulative|delta)$`, func(kind string) error {
		if w.resp == nil {
			if err := w.defaultCall(); err != nil {
				return err
			}
		}
		want := map[string]metrics.AggregationTemporality{
			"cumulative": metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			"delta":      metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		}[kind]
		counts := w.counts()
		if len(counts) == 0 {
			return failf("no count arrived")
		}
		for _, c := range counts {
			if c.temporality != want {
				return failf("got %v totals, want %s", c.temporality, kind)
			}
		}
		return nil
	})
	sc.Step(`^no histogram is sent$`, func() error {
		w.settle()
		if w.receivers[0].histograms > 0 {
			return failf("got %d histograms", w.receivers[0].histograms)
		}
		return nil
	})
	sc.Step(`^two exporters$`, func() error {
		w.receivers = append(w.receivers, newReceiver())
		return nil
	})
	sc.Step(`^both get the same record and count$`, func() error {
		w.settle()
		first, firstCounts := w.receivers[0].got()
		second, secondCounts := w.receivers[1].got()
		if len(first) != 1 || len(second) != 1 || fmt.Sprint(first[0].attrs) != fmt.Sprint(second[0].attrs) ||
			len(firstCounts) == 0 || len(secondCounts) == 0 {
			return failf("the exporters got %v and %v", first, second)
		}
		return nil
	})
	sc.Step(`^two exporters, one of them down$`, func() error {
		w.downEndpoints = []string{"http://" + freePort()}
		// The down exporter retries until a send times out, which holds up
		// the proxy's shutdown at the end of the scenario.
		w.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the other keeps receiving$`, func() error { return w.waitForRecords(2) })

	sc.Step(`^an exporter with no queue size set$`, func() error {
		w.receivers[0].hold = make(chan struct{})
		w.exportTimeout = time.Minute
		w.appID = ""
		return nil
	})
	sc.Step(`^the proxy starts$`, w.start)
	sc.Step(`^its queue holds 10 000 records$`, func() error {
		w.vendor("api.x.com")
		client := &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyURL(mustURL("http://" + w.addr)),
			MaxIdleConnsPerHost: 16,
		}}
		defer client.CloseIdleConnections()
		calls := make(chan struct{})
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				for range calls {
					req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://api.x.com/a", nil)
					resp, err := client.Do(req)
					if err == nil {
						_ = resp.Body.Close()
					}
				}
			})
		}
		for range 10_000 {
			calls <- struct{}{}
		}
		close(calls)
		wg.Wait()
		// Connections the client opened but never used would keep the
		// proxy's shutdown waiting.
		client.CloseIdleConnections()
		w.receivers[0].release()
		if got := len(w.records()); got != 10_000 {
			return failf("the receiver got %d records, want all 10 000", got)
		}
		return nil
	})

	sc.Step(`^an exporter with queueSize 3 whose destination is busy with the first record$`, func() error {
		w.exporterExtra = []string{"      queueSize: 3\n"}
		w.receivers[0].hold = make(chan struct{})
		w.exportTimeout = time.Minute
		if err := w.call("GET", "https://api.stripe.com/v1/payment_intents/pi_1"); err != nil {
			return err
		}
		return w.receivers[0].waitForLogsHeld()
	})
	sc.Step(`^4 more calls end$`, func() error {
		for i := 2; i <= 5; i++ {
			started := time.Now()
			if err := w.call("GET", fmt.Sprintf("https://api.stripe.com/v1/payment_intents/pi_%d", i)); err != nil {
				return err
			}
			w.appTook = max(w.appTook, time.Since(started))
			// A record is queued just after its reply has gone back, so
			// give it time to be queued before the next call.
			time.Sleep(50 * time.Millisecond)
		}
		w.receivers[0].release()
		return nil
	})
	sc.Step(`^the destination gets records 1, 3, 4 and 5$`, func() error {
		var got []string
		for _, r := range w.records() {
			got = append(got, sprint(r.attrs["url.full"]))
		}
		want := []string{
			"https://api.stripe.com/v1/payment_intents/pi_1", "https://api.stripe.com/v1/payment_intents/pi_3",
			"https://api.stripe.com/v1/payment_intents/pi_4", "https://api.stripe.com/v1/payment_intents/pi_5",
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return failf("the destination got %v, want %v", got, want)
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that 1 record was dropped for that destination$`, func() error {
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "dropped=1") &&
				strings.Contains(line, w.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the dropped record; the log is %s", w.logs.String())
	})
	sc.Step(`^no call was blocked or slowed$`, func() error {
		if w.appTook > time.Second {
			return failf("a call took %v", w.appTook)
		}
		return nil
	})

	sc.Step(`^the proxy runs$`, func() error {
		if w.telemetry == nil {
			w.telemetry = newReceiver()
		}
		return w.defaultCall()
	})
	sc.Step(`^records waiting in the queue$`, func() error {
		w.exporterExtra = []string{"      logInterval: 1h\n"}
		return w.defaultCall()
	})
	sc.Step(`^the proxy shuts down$`, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		err := w.proxy.Shutdown(ctx)
		w.proxy = nil
		w.appTook = time.Since(started)
		return err
	})
	sc.Step(`^they are sent first, within a time limit$`, func() error {
		if records, _ := w.receivers[0].got(); len(records) != 1 || w.appTook > 5*time.Second {
			return failf("got %d records after %v", len(records), w.appTook)
		}
		return nil
	})
}

// arrived names what the first receiver has got so far: record, span and
// count.
func (w *world) arrived() []string {
	var got []string
	records, counts := w.receivers[0].got()
	if len(records) > 0 {
		got = append(got, "record")
	}
	if len(w.receivers[0].spans()) > 0 {
		got = append(got, "span")
	}
	if len(counts) > 0 {
		got = append(got, "count")
	}
	return got
}
