package proxy_test

import (
	"cmp"
	"context"
	"fmt"
	proxy "hotline-proxy"
	"slices"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

func specLineSteps(sc *godog.ScenarioContext, sut *checkQueueSUT) {
	sc.Step(`^the proxy checks with a single worker$`, func() error {
		sut.checkWorkers = 1
		return nil
	})
	sc.Step(`^specChecks\.queueMemory is (\S+)$`, func(size string) error {
		sut.queueMemory = size
		return nil
	})
	sc.Step(`^specChecks\.queueMemory is changed to (\S+) and the proxy is told to reload$`, func(size string) error {
		sut.queueMemory = size
		return sut.proxy.reloadConfig()
	})
	sc.Step(`^the proxy is stopped with no time left$`, func() error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = sut.proxy.running.Shutdown(ctx)
		sut.proxy.running, sut.proxy.stoppedEarly = nil, true
		return nil
	})
	sc.Step(`^the proxy logs that it skipped the spec checks still waiting$`, func() error {
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "stop ran out of time, skipped the spec checks still waiting") && !strings.Contains(line, "skipped=0") {
				return nil
			}
		}
		return failf("no log says waiting checks were skipped; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^the proxy refuses the config: (.+)$`, func(problem string) error {
		_ = sut.proxy.start()
		return sut.proxy.refusedWith(problem)
	})
	sc.Step(`^the app sends a large order, then (\d+) small orders$`, func(n int) error {
		if err := sut.app.largeOrder(); err != nil {
			return err
		}
		for i := range n {
			if err := sut.order(i+1, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app sends a large order, then a small order of (\d+) KB$`, func(kb int) error {
		if err := sut.app.largeOrder(); err != nil {
			return err
		}
		return sut.order(1, strings.Repeat("x", kb<<10))
	})
	sc.Step(`^all (\d+) records say hotline\.schema\.status=valid$`, func(n int) error {
		records := sut.otlp.records()
		if len(records) != n {
			return failf("%d records arrived, want %d", len(records), n)
		}
		for _, r := range records {
			if r.attrs["hotline.schema.status"] != "valid" {
				return failf("a record says hotline.schema.status=%v, skipped %v", r.attrs["hotline.schema.status"], r.attrs["hotline.schema.skipped"])
			}
		}
		return nil
	})
	sc.Step(`^every check's trace has a "wait for check worker" step under it$`, func() error {
		spans := sut.otlp.first().spans()
		checks := 0
		for _, check := range spans {
			if check.name != "request spec check" && check.name != "response spec check" {
				continue
			}
			checks++
			if !slices.ContainsFunc(spans, func(s receivedSpan) bool {
				return s.name == "wait for check worker" && s.parent == check.spanID && s.traceID == check.traceID
			}) {
				return failf("a %q has no \"wait for check worker\" step under it", check.name)
			}
		}
		if checks == 0 {
			return failf("no spec check steps arrived")
		}
		return nil
	})
	sc.Step(`^the (first|last) small order's record says hotline\.schema\.(status|skipped)=(\S+)$`, func(which, key, value string) error {
		small := sut.smallOrders()
		if len(small) == 0 {
			return failf("no small order's record arrived")
		}
		r := small[0]
		if which == "last" {
			r = small[len(small)-1]
		}
		return has(r.attrs, "hotline.schema."+key, value)
	})
	sc.Step(`^the "wait for check worker" step of each skipped check failed with error\.type=overloaded$`, func() error {
		skipped := sut.skippedTraces()
		if len(skipped) == 0 {
			return failf("no check was skipped as overloaded")
		}
		for _, s := range sut.otlp.first().spans() {
			if s.name != "wait for check worker" || !skipped[s.traceID] || !sut.underRequestCheck(s) {
				continue
			}
			if s.status != "error" || s.attrs["error.type"] != "overloaded" {
				return failf("the wait step of a skipped check has status %s and error.type %v", s.status, s.attrs["error.type"])
			}
			delete(skipped, s.traceID)
		}
		if len(skipped) > 0 {
			return failf("%d skipped checks have no failed wait step", len(skipped))
		}
		return nil
	})
	sc.Step(`^the skipped checks' "request spec check" steps are neither ok nor failed$`, func() error {
		skipped := sut.skippedTraces()
		for _, s := range sut.otlp.first().spans() {
			if s.name == "request spec check" && skipped[s.traceID] && s.status != "unset" {
				return failf("a skipped check's step has status %s", s.status)
			}
		}
		return nil
	})
	sc.Step(`^hotline\.spec_check\.dropped counts the skipped checks$`, func() error {
		skipped := len(sut.skippedTraces())
		sut.proxy.settle()
		counted := 0.0
		for _, p := range sut.telemetry.metricPoints() {
			if p.name == "hotline.spec_check.dropped" {
				counted = max(counted, p.value)
			}
		}
		if skipped == 0 || counted != float64(skipped) {
			return failf("hotline.spec_check.dropped is %v, and %d records were skipped as overloaded", counted, skipped)
		}
		return nil
	})
}

// largeOrder sends an order with many lines, whose check keeps the single
// worker busy while the small orders after it arrive: just under the 2 MiB
// the checks read, so the check takes as long as a check can.
func (sut *appSUT) largeOrder() error {
	lines := strings.TrimSuffix(strings.Repeat(`{"sku":"S-1"},`, 140_000), ",")
	return sut.shopCall("POST", "/orders", true, "Idempotency-Key: large", "json", `{"item":"L","quantity":1,"lines":[`+lines+`]}`)
}

// order sends small order n, its item padded with pad.
func (sut *checkQueueSUT) order(n int, pad string) error {
	return sut.app.shopCall("POST", "/orders", true, "Idempotency-Key: k"+strconv.Itoa(n), "json",
		fmt.Sprintf(`{"item":"A%d%s","quantity":%d}`, n, pad, n))
}

// smallOrders are the records of the orders sent after the large one, in
// the order they were sent.
func (sut *checkQueueSUT) smallOrders() []receivedRecord {
	var small []receivedRecord
	for _, r := range sut.otlp.records() {
		if size, _ := r.attrs["http.request.body.size"].(int64); size < 100_000 {
			small = append(small, r)
		}
	}
	slices.SortFunc(small, func(a, b receivedRecord) int { return cmp.Compare(a.timestamp, b.timestamp) })
	return small
}

// skippedTraces are the traces of the calls whose checks were skipped as
// overloaded.
func (sut *checkQueueSUT) skippedTraces() map[string]bool {
	skipped := map[string]bool{}
	for _, r := range sut.otlp.records() {
		if r.attrs["hotline.schema.skipped"] == "overloaded" {
			skipped[r.traceID] = true
		}
	}
	return skipped
}

// underRequestCheck says whether a span sits under its trace's request spec
// check.
func (sut *checkQueueSUT) underRequestCheck(s receivedSpan) bool {
	return slices.ContainsFunc(sut.otlp.first().spans(), func(p receivedSpan) bool {
		return p.name == "request spec check" && p.spanID == s.parent && p.traceID == s.traceID
	})
}

// checkQueueSUT is the line of spec checks waiting for a check worker:
// how many workers there are, how much memory the waiting checks may hold,
// and what is skipped when it is full.
type checkQueueSUT struct {
	proxy     *proxySUT
	vendors   *vendorsSUT
	app       *appSUT
	otlp      *otlpSUT
	telemetry *telemetrySUT
	// checkWorkers is how many checks run at once; the proxy's own number
	// when 0.
	checkWorkers int
	// queueMemory is the config's specChecks.queueMemory, when set.
	queueMemory string
}

func newCheckQueueSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT, otlp *otlpSUT, telemetry *telemetrySUT) *checkQueueSUT {
	sut := &checkQueueSUT{proxy: root, vendors: vendors, app: app, otlp: otlp, telemetry: telemetry}
	root.addConfig(configSpecChecks, func() string {
		if sut.queueMemory == "" {
			return ""
		}
		return "specChecks:\n  queueMemory: " + sut.queueMemory + "\n"
	})
	root.addOption(func(o *proxy.Options) { o.CheckWorkers = sut.checkWorkers })
	return sut
}

// AddSteps adds the steps of the spec check line.
func (sut *checkQueueSUT) AddSteps(sc *godog.ScenarioContext) {
	memorySteps(sc, sut)
	specLineSteps(sc, sut)
}
