package proxy_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

func specLineSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the proxy checks with a single worker$`, func() error {
		w.checkWorkers = 1
		return nil
	})
	sc.Step(`^specChecks\.queueMemory is (\S+)$`, func(size string) error {
		w.topExtra = "specChecks:\n  queueMemory: " + size + "\n"
		return nil
	})
	sc.Step(`^specChecks\.queueMemory is changed to (\S+) and the proxy is told to reload$`, func(size string) error {
		w.topExtra = "specChecks:\n  queueMemory: " + size + "\n"
		w.replaceFile("hotline-proxy.yaml", w.config())
		w.proxy.Reload()
		return nil
	})
	sc.Step(`^the proxy is stopped with no time left$`, func() error {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = w.proxy.Shutdown(ctx)
		w.proxy, w.stoppedEarly = nil, true
		return nil
	})
	sc.Step(`^the proxy logs that it skipped the spec checks still waiting$`, func() error {
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "stop ran out of time, skipped the spec checks still waiting") && !strings.Contains(line, "skipped=0") {
				return nil
			}
		}
		return failf("no log says waiting checks were skipped; the log is %s", w.logs.String())
	})
	sc.Step(`^the proxy refuses the config: (.+)$`, func(problem string) error {
		_ = w.start()
		return w.refusedWith(problem)
	})
	sc.Step(`^the app sends a large order, then (\d+) small orders$`, func(n int) error {
		if err := w.largeOrder(); err != nil {
			return err
		}
		for i := range n {
			if err := w.order(i+1, ""); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the app sends a large order, then a small order of (\d+) KB$`, func(kb int) error {
		if err := w.largeOrder(); err != nil {
			return err
		}
		return w.order(1, strings.Repeat("x", kb<<10))
	})
	sc.Step(`^all (\d+) records say hotline\.schema\.status=valid$`, func(n int) error {
		records := w.records()
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
		spans := w.receivers[0].spans()
		checks := 0
		for _, check := range spans {
			if check.name != "request spec check" && check.name != "reply spec check" {
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
		small := w.smallOrders()
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
		skipped := w.skippedTraces()
		if len(skipped) == 0 {
			return failf("no check was skipped as overloaded")
		}
		for _, s := range w.receivers[0].spans() {
			if s.name != "wait for check worker" || !skipped[s.traceID] || !w.underRequestCheck(s) {
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
		skipped := w.skippedTraces()
		for _, s := range w.receivers[0].spans() {
			if s.name == "request spec check" && skipped[s.traceID] && s.status != "unset" {
				return failf("a skipped check's step has status %s", s.status)
			}
		}
		return nil
	})
	sc.Step(`^hotline\.spec_check\.dropped counts the skipped checks$`, func() error {
		skipped := len(w.skippedTraces())
		w.settle()
		counted := 0.0
		for _, p := range w.telemetry.metricPoints() {
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
func (w *world) largeOrder() error {
	lines := strings.TrimSuffix(strings.Repeat(`{"sku":"S-1"},`, 140_000), ",")
	return w.shopCall("POST", "/orders", true, "Idempotency-Key: large", "json", `{"item":"L","quantity":1,"lines":[`+lines+`]}`)
}

// order sends small order n, its item padded with pad.
func (w *world) order(n int, pad string) error {
	return w.shopCall("POST", "/orders", true, "Idempotency-Key: k"+strconv.Itoa(n), "json",
		fmt.Sprintf(`{"item":"A%d%s","quantity":%d}`, n, pad, n))
}

// smallOrders are the records of the orders sent after the large one, in
// the order they were sent.
func (w *world) smallOrders() []receivedRecord {
	var small []receivedRecord
	for _, r := range w.records() {
		if size, _ := r.attrs["http.request.body.size"].(int64); size < 100_000 {
			small = append(small, r)
		}
	}
	slices.SortFunc(small, func(a, b receivedRecord) int { return cmp.Compare(a.timestamp, b.timestamp) })
	return small
}

// skippedTraces are the traces of the calls whose checks were skipped as
// overloaded.
func (w *world) skippedTraces() map[string]bool {
	skipped := map[string]bool{}
	for _, r := range w.records() {
		if r.attrs["hotline.schema.skipped"] == "overloaded" {
			skipped[r.traceID] = true
		}
	}
	return skipped
}

// underRequestCheck says whether a span sits under its trace's request spec
// check.
func (w *world) underRequestCheck(s receivedSpan) bool {
	return slices.ContainsFunc(w.receivers[0].spans(), func(p receivedSpan) bool {
		return p.name == "request spec check" && p.spanID == s.parent && p.traceID == s.traceID
	})
}
