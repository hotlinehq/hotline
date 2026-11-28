package proxy_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	proxy "hotline-proxy"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

func exportSteps(sc *godog.ScenarioContext, sut *otlpSUT) {
	sc.Step(`^a fake OTLP receiver$`, func() error { return nil })
	sc.Step(`^the proxy sends any signal$`, func() error { return sut.app.defaultCall() })
	sc.Step(`^its resource has service.name hotline-proxy, service.version and service.instance.id$`, func() error {
		records := sut.records()
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
	sc.Step(`^the records transform labels service.name invoices-proxy, service.instance.id from the env POD_NAME and deployment.environment.name from the file env-name$`, func() error {
		sut.proxy.setEnv("POD_NAME", "invoices-proxy-7d9f")
		file := sut.proxy.writeFile("env-name", "production\n")
		sut.addLabels("service.name: invoices-proxy", "service.instance.id: { env: POD_NAME }",
			"deployment.environment.name: { file: "+file+" }")
		return nil
	})
	sc.Step(`^the records transform labels deployment.environment.name staging(, then deployment.environment.name production)?$`, func(then string) error {
		sut.addLabels("deployment.environment.name: staging")
		if then != "" {
			sut.addLabels("deployment.environment.name: production")
		}
		return nil
	})
	sc.Step(`^the records transform labels service.name shop-proxy$`, func() error {
		sut.addLabels("service.name: shop-proxy")
		return nil
	})
	sc.Step(`^its resource has deployment.environment.name production$`, func() error {
		records := sut.records()
		if len(records) == 0 {
			return failf("no record arrived")
		}
		return has(records[0].resource, "deployment.environment.name", "production")
	})
	sc.Step(`^its resource has service.name invoices-proxy, service.instance.id from POD_NAME and deployment.environment.name from the file$`, func() error {
		records := sut.records()
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
		return has(sut.records()[0].resource, "service.version", "1.0.0-test")
	})
	sc.Step(`^the record's level is (INFO|WARN|ERROR)$`, func(level string) error {
		records := sut.records()
		if len(records) == 0 {
			return failf("no record arrived; the app got %v %q", sut.app.resp, sut.app.respBody)
		}
		last := records[len(records)-1]
		numbers := map[string]int32{"INFO": 9, "WARN": 13, "ERROR": 17}
		if last.severityText != level || last.severityNumber != numbers[level] {
			return failf("the level is %q (%d), want %s (%d)", last.severityText, last.severityNumber, level, numbers[level])
		}
		return nil
	})
	sc.Step(`^the receiver gets one OTLP log record with all of the record's attributes$`, func() error {
		record, err := sut.record()
		if err != nil {
			return err
		}
		return hasAll(record, "hotline.integration.id", "hotline.ingress", "url.full", "url.template",
			"http.response.status_code", "hotline.timing.total_us", "hotline.config.version")
	})
	sc.Step(`^standard names come from the semconv constants, such as http.request.method and url.full$`, func() error {
		return sut.lastHasAll("http.request.method", "url.full", "url.scheme", "client.address", "server.address", "server.port",
			"network.peer.address", "network.protocol.version", "http.request.body.size", "http.response.body.size",
			"tls.protocol.version", "tls.server.not_after")
	})
	sc.Step(`^every other field has the exact hotline. name from the spec$`, func() error {
		record, err := sut.lastRecord()
		if err != nil {
			return err
		}
		standard := []string{"http.request.method", "url.full", "url.scheme", "url.template", "client.address", "server.address",
			"server.port", "network.peer.address", "network.protocol.version", "http.response.status_code",
			"error.type", "http.request.body.size", "http.response.body.size", "tls.protocol.version",
			"tls.server.not_after"}
		hotline := []string{"hotline.integration.id", "hotline.ingress", "hotline.timing.total_us",
			"hotline.client.cancelled", "hotline.config.version", "hotline.trace.hash", "hotline.schema.request.errors",
			"hotline.schema.response.errors", "hotline.schema.request.errors_total", "hotline.schema.response.errors_total",
			"hotline.schema.skipped", "hotline.schema.status", "hotline.record.hash", "hotline.batch.id",
			"hotline.batch.index", "hotline.request.body.hash", "hotline.response.body.hash"}
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
		record, err := sut.lastRecord()
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

	sc.Step(`^the receiver gets nothing on /v1/metrics$`, func() error {
		sut.proxy.settle()
		for _, seen := range sut.receivers[0].requests() {
			if strings.HasSuffix(seen.path, "/v1/metrics") {
				return failf("the receiver got a send on %s", seen.path)
			}
		}
		return nil
	})
	sc.Step(`^an exporter that sets metricInterval to 0s and temporality to weekly$`, func() error {
		sut.exporterExtra = []string{"      metricInterval: 0s\n      temporality: weekly\n"}
		return nil
	})
	sc.Step(`^the proxy logs no warning about metricInterval or temporality$`, func() error {
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && (strings.Contains(line, "metricInterval") || strings.Contains(line, "temporality")) {
				return failf("the proxy warned: %s", line)
			}
		}
		return nil
	})
	sc.Step(`^an exporter that sets no intervals$`, func() error {
		sut.unsetIntervals = true
		return nil
	})
	sc.Step(`^an exporter that sets (logInterval|traceInterval) to 1h$`, func(name string) error {
		sut.exporterExtra = []string{"      " + name + ": 1h\n"}
		return nil
	})
	sc.Step(`^nothing arrives within a second$`, func() error {
		pause(time.Second)
		if got := sut.arrived(); len(got) > 0 {
			return failf("got %v within a second", got)
		}
		return nil
	})
	sc.Step(`^the record and its spans arrive when the proxy stops$`, func() error {
		sut.proxy.settle()
		if got := sut.arrived(); len(got) != 2 {
			return failf("got only %v", got)
		}
		return nil
	})
	sc.Step(`^no (record|span) arrives within a second, but the others do$`, func(signal string) error {
		err := sut.proxy.eventually(func() error {
			if got := sut.arrived(); len(got) < 1 {
				return failf("got only %v", got)
			}
			return nil
		})
		if err != nil {
			return err
		}
		pause(time.Second)
		if got := sut.arrived(); slices.Contains(got, signal) {
			return failf("the %s arrived within a second", signal)
		}
		return nil
	})
	sc.Step(`^the (record|span) arrives when the proxy stops$`, func(signal string) error {
		sut.proxy.settle()
		if got := sut.arrived(); !slices.Contains(got, signal) {
			return failf("no %s arrived, got %v", signal, got)
		}
		return nil
	})
	sc.Step(`^calls end$`, func() error {
		for range 2 {
			if err := sut.app.defaultCall(); err != nil {
				return err
			}
			pause(50 * time.Millisecond)
		}
		return nil
	})
	sc.Step(`^two exporters$`, func() error {
		sut.receivers = append(sut.receivers, newReceiver())
		return nil
	})
	sc.Step(`^both get the same record and spans$`, func() error {
		sut.proxy.settle()
		first, second := sut.receivers[0].got(), sut.receivers[1].got()
		if len(first) != 1 || len(second) != 1 || fmt.Sprint(first[0].attrs) != fmt.Sprint(second[0].attrs) ||
			len(sut.receivers[0].spans()) == 0 || len(sut.receivers[0].spans()) != len(sut.receivers[1].spans()) {
			return failf("the exporters got %v and %v", first, second)
		}
		return nil
	})
	sc.Step(`^two exporters, one of them down$`, func() error {
		sut.downEndpoints = []string{"http://" + sut.proxy.ports.take()}
		// The down exporter retries until a send times out, which holds up
		// the proxy's shutdown at the end of the scenario.
		sut.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the other keeps receiving$`, func() error { return sut.waitForRecords(2) })

	sc.Step(`^an exporter with no queue size set$`, func() error {
		sut.receivers[0].hold = make(chan struct{})
		sut.exportTimeout = time.Minute
		sut.app.appID = ""
		return nil
	})
	sc.Step(`^the proxy starts$`, sut.proxy.start)
	sc.Step(`^its queue holds 10 000 records$`, func() error {
		sut.vendors.vendor("api.x.com")
		client := &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyURL(mustURL("http://" + sut.proxy.addr)),
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
		sut.receivers[0].release()
		if got := len(sut.records()); got != 10_000 {
			return failf("the receiver got %d records, want all 10 000", got)
		}
		return nil
	})

	sc.Step(`^an exporter with queueSize 3 whose exporter is busy with the first record$`, func() error {
		sut.exporterExtra = []string{"      queueSize: 3\n"}
		sut.receivers[0].hold = make(chan struct{})
		sut.exportTimeout = time.Minute
		if err := sut.app.call("GET", "https://api.stripe.com/v1/payment_intents/pi_1"); err != nil {
			return err
		}
		return sut.receivers[0].waitForLogsHeld()
	})
	sc.Step(`^(\d+) more calls end$`, func(n int) error {
		for i := 2; i <= n+1; i++ {
			started := time.Now()
			if err := sut.app.call("GET", fmt.Sprintf("https://api.stripe.com/v1/payment_intents/pi_%d", i)); err != nil {
				return err
			}
			sut.app.appTook = max(sut.app.appTook, time.Since(started))
			// A record is queued just after its response has gone back, so
			// give it time to be queued before the next call.
			pause(50 * time.Millisecond)
		}
		sut.receivers[0].release()
		return nil
	})
	sc.Step(`^the exporter gets records 1, 2 and 3$`, func() error {
		var got []string
		for _, r := range sut.records() {
			got = append(got, sprint(r.attrs["url.full"]))
		}
		want := []string{
			"https://api.stripe.com/v1/payment_intents/pi_1", "https://api.stripe.com/v1/payment_intents/pi_2",
			"https://api.stripe.com/v1/payment_intents/pi_3",
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return failf("the exporter got %v, want %v", got, want)
		}
		return nil
	})
	sc.Step(`^an exporter with queueSize (\d+) that sends only on shutdown$`, func(size int) error {
		sut.exporterExtra = []string{fmt.Sprintf("      queueSize: %d\n", size), "      logInterval: 1h\n", "      traceInterval: 1h\n"}
		return nil
	})
	sc.Step(`^2 calls end$`, func() error {
		for range 2 {
			if err := sut.app.defaultCall("Content-Type: application/json"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the exporter gets every span of the first call$`, func() error {
		records := sut.receivers[0].got()
		if len(records) != 2 {
			return failf("the exporter got %d records, want 2", len(records))
		}
		first := records[0].traceID
		spans := sut.receivers[0].spans()
		ids := map[string]bool{}
		for _, s := range spans {
			if s.traceID == first {
				ids[s.spanID] = true
			}
		}
		if len(ids) == 0 {
			return failf("the exporter got no span of the first call; it got %d spans", len(spans))
		}
		for _, s := range spans {
			if s.traceID != first {
				continue
			}
			if s.name != "hotline call" && !ids[s.parent] {
				return failf("span %q of the first call came without its parent", s.name)
			}
		}
		return nil
	})
	sc.Step(`^it gets no span of the second call$`, func() error {
		records := sut.receivers[0].got()
		second := records[1].traceID
		for _, s := range sut.receivers[0].spans() {
			if s.traceID == second {
				return failf("the exporter got span %q of the second call", s.name)
			}
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that spans of 1 call were dropped for that exporter$`, func() error {
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "spans") && strings.Contains(line, "calls=1") &&
				strings.Contains(line, sut.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the dropped call; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^the exporter gets no span$`, func() error {
		if spans := sut.receivers[0].spans(); len(spans) != 0 {
			return failf("the exporter got %d spans", len(spans))
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that (1 record was|2 records were) dropped for that exporter$`, func(how string) error {
		dropped := "dropped=" + how[:1] + " "
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line+" ", dropped) &&
				strings.Contains(line, sut.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the dropped record; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^no call was blocked or slowed$`, func() error {
		if sut.app.appTook > time.Second {
			return failf("a call took %v", sut.app.appTook)
		}
		return nil
	})

	sc.Step(`^records waiting in the queue$`, func() error {
		sut.exporterExtra = []string{"      logInterval: 1h\n"}
		return sut.app.defaultCall()
	})
	sc.Step(`^the proxy shuts down$`, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		err := sut.proxy.running.Shutdown(ctx)
		sut.proxy.running = nil
		sut.app.appTook = time.Since(started)
		return err
	})
	sc.Step(`^they are sent first, within a time limit$`, func() error {
		if records := sut.receivers[0].got(); len(records) != 1 || sut.app.appTook > 5*time.Second {
			return failf("got %d records after %v", len(records), sut.app.appTook)
		}
		return nil
	})
}

// arrived names what the first receiver has got so far: record and span.
func (sut *otlpSUT) arrived() []string {
	var got []string
	if len(sut.receivers[0].got()) > 0 {
		got = append(got, "record")
	}
	if len(sut.receivers[0].spans()) > 0 {
		got = append(got, "span")
	}
	return got
}

var errNoRecord = errors.New("no record arrived")

// otlpSUT is the OTLP exporters of the config, each with the fake
// receiver it sends to, and the records they got.
type otlpSUT struct {
	proxy   *proxySUT
	app     *appSUT
	vendors *vendorsSUT
	apis    *apisSUT
	// receivers are the OTLP exporters, in the order of the config; the
	// first is made by Open.
	receivers []*receiver
	// downEndpoints are exporter endpoints where nothing listens.
	downEndpoints []string
	// exporterExtra are more lines for the exporter of each receiver.
	exporterExtra []string
	// unsetIntervals leaves the intervals out of the exporters, so the
	// defaults apply. Otherwise everything is sent every 20ms, to keep
	// tests quick.
	unsetIntervals bool
	// exportTimeout is how long one send may take; 2s unless set, so a slow
	// first connection under the race detector still gets through.
	exportTimeout time.Duration
	// labelSets are the records transform's steps, each the lines of one
	// labels step.
	labelSets []string
}

func newOTLPSUT(root *proxySUT, app *appSUT, vendors *vendorsSUT, apis *apisSUT) *otlpSUT {
	sut := &otlpSUT{proxy: root, app: app, vendors: vendors, apis: apis}
	root.addConfig(configExporters, sut.exporters)
	root.addConfig(configRecords, sut.recordsSection)
	root.addOption(func(o *proxy.Options) { o.ExportTimeout = cmp.Or(sut.exportTimeout, 2*time.Second) })
	return sut
}

// addLabels adds a labels step to the records transform, with lines
// such as "service.name: shop-proxy", one per label.
func (sut *otlpSUT) addLabels(lines ...string) {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("        " + line + "\n")
	}
	sut.labelSets = append(sut.labelSets, b.String())
}

// recordsSection is the records section, with the transform's steps.
func (sut *otlpSUT) recordsSection() string {
	if len(sut.labelSets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("records:\n  transform:\n")
	for _, labels := range sut.labelSets {
		b.WriteString("    - labels:\n" + labels)
	}
	return b.String()
}

// Open starts the receiver of the first exporter.
func (sut *otlpSUT) Open() {
	sut.receivers = []*receiver{newReceiver()}
}

// Close stops the receivers.
func (sut *otlpSUT) Close() {
	for _, r := range sut.receivers {
		r.stop()
	}
}

// AddSteps adds the steps of the OTLP exporters and their records.
func (sut *otlpSUT) AddSteps(sc *godog.ScenarioContext) {
	exportSteps(sc, sut)
	diskQueueSteps(sc, sut)
	recordSteps(sc, sut)
	traceSteps(sc, sut)
	protocolSteps(sc, sut)
}

// exporters are the config's OTLP exporters.
func (sut *otlpSUT) exporters() string {
	var b strings.Builder
	for i, r := range sut.receivers {
		b.WriteString("  - otlp:\n")
		fmt.Fprintf(&b, "      endpoint: %s\n", r.endpoint)
		extra := ""
		if i < len(sut.exporterExtra) {
			extra = sut.exporterExtra[i]
		}
		b.WriteString(extra)
		for _, name := range []string{"logInterval", "traceInterval"} {
			if !sut.unsetIntervals && !strings.Contains(extra, name) {
				b.WriteString("      " + name + ": 20ms\n")
			}
		}
	}
	for _, e := range sut.downEndpoints {
		fmt.Fprintf(&b, "  - otlp:\n      endpoint: %s\n", e)
	}
	return b.String()
}

// records are the hotline.request records the first receiver got, once the
// proxy has sent everything.
func (sut *otlpSUT) records() []receivedRecord {
	sut.proxy.settle()
	records := sut.receivers[0].got()
	return records
}

// record is the only record of the scenario.
func (sut *otlpSUT) record() (map[string]any, error) {
	records := sut.records()
	if len(records) != 1 {
		return nil, fmt.Errorf("%w: want exactly one record, got %d", errNoRecord, len(records))
	}
	return records[0].attrs, nil
}

// waitForRecords waits until the first receiver has n records, while the
// proxy keeps running.
func (sut *otlpSUT) waitForRecords(n int) error {
	if !eventually(waitLimit, func() bool { return len(sut.receivers[0].got()) >= n }) {
		return failf("%d records did not arrive", n)
	}
	return nil
}

// first is the receiver of the first OTLP exporter, and second of the
// second.
func (sut *otlpSUT) first() *receiver  { return sut.receivers[0] }
func (sut *otlpSUT) second() *receiver { return sut.receivers[1] }

// all are the receivers of every OTLP exporter, in the order of the config.
func (sut *otlpSUT) all() []*receiver { return sut.receivers }

// hasExporters says whether the config has any OTLP exporter.
func (sut *otlpSUT) hasExporters() bool { return len(sut.receivers) > 0 }

// addExporter adds an OTLP exporter, with a receiver of its own.
func (sut *otlpSUT) addExporter() { sut.receivers = append(sut.receivers, newReceiver()) }

// replaceFirst gives the first OTLP exporter another receiver.
func (sut *otlpSUT) replaceFirst(r *receiver) { sut.receivers[0] = r }

// removeExporters stops every receiver and leaves the OTLP exporters out of
// the config.
func (sut *otlpSUT) removeExporters() {
	for _, r := range sut.receivers {
		r.stop()
	}
	sut.receivers = nil
}

// setFirstExporterLines gives the first OTLP exporter more lines of config.
func (sut *otlpSUT) setFirstExporterLines(lines string) { sut.exporterExtra = []string{lines} }

// addExporterThatIsDown adds an OTLP exporter whose endpoint nothing
// listens on.
func (sut *otlpSUT) addExporterThatIsDown() {
	sut.downEndpoints = append(sut.downEndpoints, "http://"+sut.proxy.ports.take())
}

// checkResults are the record's spec check results on one side, each
// written as "part place rule expected".
func (sut *otlpSUT) checkResults(side string) ([]string, error) {
	record, err := sut.record()
	if err != nil {
		return nil, err
	}
	return resultsOf(record, side)
}
