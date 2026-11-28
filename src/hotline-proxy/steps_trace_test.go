package proxy_test

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

func traceSteps(sc *godog.ScenarioContext, sut *otlpSUT) {
	sc.Step(`^the integration's traceFormat is (\w+)$`, func(format string) error {
		sut.apis.integrations[len(sut.apis.integrations)-1] += "    traceFormat: " + format + "\n"
		return nil
	})
	sc.Step(`^the integration's traceForward is on, with traceFormat (\w+)$`, func(format string) error {
		sut.apis.integrations[len(sut.apis.integrations)-1] += "    traceForward: true\n    traceFormat: " + format + "\n"
		return nil
	})
	sc.Step(`^a not-passed entry with traceFormat (\w+)$`, func(format string) error {
		sut.apis.addIntegration("  - id: not-passed\n    traceFormat: " + format + "\n")
		return nil
	})
	sc.Step(`^the record's trace_id is (\w+) and its flags are (\d+)$`, func(traceID, flags string) error {
		r, err := sut.lastReceived()
		if err != nil {
			return err
		}
		if r.traceID != traceID || fmt.Sprintf("%02x", r.flags) != flags {
			return failf("the record has trace_id %q and flags %02x, want %s and %s", r.traceID, r.flags, traceID, flags)
		}
		return nil
	})
	sc.Step(`^the record's span_id is new$`, func() error {
		r, err := sut.lastReceived()
		if err != nil {
			return err
		}
		if len(r.spanID) != 16 || r.spanID == strings.Repeat("0", 16) || strings.Contains(sut.sentHeaders(), r.spanID) {
			return failf("the record's span_id is %q, want a new one", r.spanID)
		}
		return nil
	})
	sc.Step(`^the record has a new trace_id, with flags 01$`, func() error {
		r, err := sut.lastReceived()
		if err != nil {
			return err
		}
		if len(r.traceID) != 32 || r.traceID == strings.Repeat("0", 32) || r.flags != 1 ||
			strings.Contains(sut.sentHeaders(), r.traceID) || strings.Contains(sut.sentHeaders(), r.traceID[16:]) {
			return failf("the record has trace_id %q and flags %02x, want a new one with flags 01", r.traceID, r.flags)
		}
		return nil
	})
	sc.Step(`^the app sends GET /orders with the trace headers "([^"]*)"$`, func(headers string) error {
		return sut.app.shopCall("GET", "/orders", true, headers, "", "")
	})
	sc.Step(`^the app sends two calls without a trace header$`, func() error {
		for range 2 {
			if err := sut.app.shopCall("GET", "/orders", true, "", "", ""); err != nil {
				return err
			}
		}
		return sut.waitForRecords(2)
	})
	sc.Step(`^their records have different trace_ids$`, func() error {
		rs := sut.records()
		if len(rs) != 2 || rs[0].traceID == rs[1].traceID || rs[0].spanID == rs[1].spanID {
			return failf("the records are %+v", rs)
		}
		return nil
	})
	sc.Step(`^the record's trace_id starts with the call's time in seconds, as X-Ray needs$`, func() error {
		r, err := sut.lastReceived()
		if err != nil {
			return err
		}
		seconds, _ := strconv.ParseUint(r.traceID[:8], 16, 32)
		if at := r.timestamp / 1e9; seconds+1 < at || seconds > at+1 {
			return failf("the trace_id %s starts with %d, want about %d", r.traceID, seconds, at)
		}
		return nil
	})
	sc.Step(`^the vendor gets the trace header "([^"]+)"$`, func(header string) error {
		name, value, _ := strings.Cut(header, ": ")
		got := sut.shopVendorCall().header.Values(name)
		if len(got) != 1 || got[0] != value {
			return failf("the vendor got %s %q, want %q", name, got, value)
		}
		return nil
	})
	sc.Step(`^the vendor gets no (\S+) header$`, func(name string) error {
		if got := sut.shopVendorCall().header.Values(name); len(got) > 0 {
			return failf("the vendor got %s %q", name, got)
		}
		return nil
	})
	sc.Step(`^the vendor gets (.+), with the record's trace_id and the "vendor call" span's id$`, func(headers string) error {
		t, err := sut.lastTrace()
		if err != nil {
			return err
		}
		vendor, err := t.one("vendor call")
		if err != nil {
			return err
		}
		r := t.record
		trace := r.traceID
		if strings.HasPrefix(headers, "X-Amzn-Trace-Id") {
			trace = trace[:8] + "-" + trace[8:]
		}
		got := sut.shopVendorCall().header
		for _, h := range splitHeaders(strings.NewReplacer("TRACE", trace, "SPAN", vendor.spanID).Replace(headers)) {
			name, value, _ := strings.Cut(h, ": ")
			if values := got.Values(name); len(values) != 1 || values[0] != value {
				return failf("the vendor got %s %q, want %q", name, values, value)
			}
		}
		return nil
	})
	sc.Step(`^the vendor gets none of (.+)$`, func(names string) error {
		got := sut.shopVendorCall().header
		for _, name := range strings.Split(names, ", ") {
			if values := got.Values(name); len(values) > 0 {
				return failf("the vendor got %s %q", name, values)
			}
		}
		return nil
	})
}

// lastReceived is the last record the receiver got, with its trace fields.
func (sut *otlpSUT) lastReceived() (receivedRecord, error) {
	records := sut.records()
	if len(records) == 0 {
		return receivedRecord{}, failf("no record arrived; the app got %v %q", sut.app.resp, sut.app.respBody)
	}
	return records[len(records)-1], nil
}

// shopVendorCall is the last call the shop's vendor got.
func (sut *otlpSUT) shopVendorCall() vendorCall {
	calls := sut.vendors.vendor("api.shop.test").received()
	if len(calls) == 0 {
		return vendorCall{}
	}
	return calls[len(calls)-1]
}

// sentHeaders is the headers the app sent on its last call, in lower case,
// to tell new ids from the app's own.
func (sut *otlpSUT) sentHeaders() string {
	return strings.ToLower(strings.Join(sut.app.lastSent, "\n"))
}
