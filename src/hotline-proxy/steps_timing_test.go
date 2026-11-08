package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func timingSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a vendor reached over a new https connection$`, func() error {
		w.network.lookupTime = 20 * time.Millisecond
		return nil
	})
	sc.Step(`^a call ends$`, func() error { return w.defaultCall("Content-Type: application/json") })
	sc.Step(`^a call ends with "([^"]+)"$`, func(header string) error { return w.defaultCall(header) })
	sc.Step(`^the call ends$`, func() error {
		if w.resp != nil || w.respErr != nil {
			return nil //nolint:nilerr // the call already ended; a broken reply is what some scenarios check
		}
		if w.pending != nil {
			return w.send(*w.pending)
		}
		return w.defaultCall()
	})
	sc.Step(`^network.peer.address holds the vendor IP$`, func() error {
		return w.lastHas("network.peer.address", w.vendor("api.talon.one").ip.String())
	})

	sc.Step(`^the trace has (?:the spans|only the span) (.+?)(, with status error)?$`, func(list, failed string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		want := quoted(list)
		var got []string
		for _, s := range t.spans {
			got = append(got, s.name)
		}
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			return failf("the trace has the spans %q, want %q", got, want)
		}
		if failed != "" && t.call().status != "error" {
			return failf("the call span's status is %s, want error", t.call().status)
		}
		return nil
	})
	sc.Step(`^(.+) are under "([^"]+)", one after another$`, func(list, parent string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		p, err := t.one(parent)
		if err != nil {
			return err
		}
		want := quoted(list)
		var children []receivedSpan
		for _, c := range t.children(p) {
			if slices.Contains(want, c.name) {
				children = append(children, c)
			}
		}
		var got []string
		for i, c := range children {
			got = append(got, c.name)
			if i > 0 && c.start < children[i-1].end {
				return failf("%q starts before %q ends", c.name, children[i-1].name)
			}
		}
		if strings.Join(got, ", ") != strings.Join(want, ", ") {
			return failf("under %q are %q, want %q", parent, got, want)
		}
		return nil
	})
	sc.Step(`^every span( but the spec checks)? is within its parent$`, func(except string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			if except != "" && strings.HasSuffix(s.name, " spec check") {
				continue
			}
			for _, p := range t.spans {
				if p.spanID == s.parent && (s.start < p.start || s.end > p.end) {
					return failf("%q runs from %d to %d, outside %q from %d to %d", s.name, s.start, s.end, p.name, p.start, p.end)
				}
			}
		}
		return nil
	})
	sc.Step(`^"([^"]+)" starts when "([^"]+)" ends(?:, under "([^"]+)")?$`, func(later, earlier, parent string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		l, err := t.one(later)
		if err != nil {
			return err
		}
		e, err := t.one(earlier)
		if err != nil {
			return err
		}
		if l.start != e.end || l.end < l.start {
			return failf("%q runs from %d to %d; %q ends at %d", later, l.start, l.end, earlier, e.end)
		}
		if parent == "" {
			return nil
		}
		p, err := t.one(parent)
		if err != nil {
			return err
		}
		if l.parent != p.spanID {
			return failf("%q is not under %q", later, parent)
		}
		return nil
	})
	sc.Step(`^"hotline call" is a server span, "vendor call" a client span, and the others internal$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			want := map[string]string{"hotline call": "server", "vendor call": "client"}[s.name]
			if want == "" {
				want = "internal"
			}
			if s.kind != want {
				return failf("%q is a %s span, want %s", s.name, s.kind, want)
			}
		}
		return nil
	})
	sc.Step(`^"([^"]+)" has ([a-z_]+\.[a-z_.]+) (\S+)$`, func(name, key, value string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		s, err := t.one(name)
		if err != nil {
			return err
		}
		return has(s.attrs, key, value)
	})
	sc.Step(`^no other span has an attribute$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		allowed := map[string]string{"dns lookup": "dns.question.name", "vendor call": "hotline.connection.reused"}
		for _, s := range t.spans {
			for key := range s.attrs {
				if allowed[s.name] != key {
					return failf("%q has the attribute %s", s.name, key)
				}
			}
		}
		return nil
	})
	sc.Step(`^the record's hotline.timing.total_us is the length of "hotline call"$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		call := t.call()
		return has(t.record.attrs, "hotline.timing.total_us", (call.end-call.start)/1000)
	})
	sc.Step(`^the record has no other hotline.timing attribute$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		for key := range record {
			if strings.HasPrefix(key, "hotline.timing.") && key != "hotline.timing.total_us" {
				return failf("the record has %s", key)
			}
		}
		return nil
	})
	sc.Step(`^the record has no hotline.connection.reused or dns.question.name$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "hotline.connection.reused", "dns.question.name")
	})

	sc.Step(`^the last byte of the reply has gone to the app$`, func() error {
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(rw, "first part ")
			rw.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
			_, _ = io.WriteString(rw, "last part")
		})
		started := time.Now()
		err := w.defaultCall()
		w.appTook = time.Since(started)
		return err
	})
	sc.Step(`^"hotline call" ends at that moment$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		call := t.call()
		reply, err := t.one("receive reply")
		if err != nil {
			return err
		}
		if call.end < reply.start+int64(140*time.Millisecond) || time.Duration(call.end-call.start) > w.appTook {
			return failf("the call span ends %v after the reply started, and lasts %v; want it after the last part and within the app's %v",
				time.Duration(call.end-reply.start), time.Duration(call.end-call.start), w.appTook)
		}
		return nil
	})
	sc.Step(`^"receive reply" lasts at least as long as the vendor took to send the rest$`, func() error {
		return w.lasts("receive reply", 140*time.Millisecond, time.Hour)
	})

	sc.Step(`^a vendor reached over plain http$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("    tlsOrigination: false\n")))
		w.target = "http://api.talon.one/v2/customer_sessions/sess-8812"
		return nil
	})
	sc.Step(`^the trace has no (.+) span$`, func(list string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		return t.hasNone(quoted(list)...)
	})
	sc.Step(`^a vendor whose host is an IP address$`, func() error {
		w.appID = ""
		w.target = "http://" + w.vendor("api.talon.one").ip.String() + "/v2/x"
		return nil
	})

	sc.Step(`^a connection to the vendor that is already open$`, func() error {
		if err := w.defaultCall(); err != nil {
			return err
		}
		return w.waitForRecords(1)
	})
	sc.Step(`^a call that waits for a new connection while another call holds the only open one$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("    tlsOrigination: false\n")))
		w.freed = make(chan struct{})
		freed := w.freed
		w.vendor("api.talon.one").setAnswer(func(_ http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/first") {
				<-freed
				return
			}
			// Long enough for the background connection to finish opening.
			time.Sleep(300 * time.Millisecond)
		})
		return w.start()
	})
	sc.Step(`^the other call ends first and frees its connection$`, func() error {
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL("http://" + w.addr))}}
		defer client.CloseIdleConnections()
		get := func(path string) error {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://api.talon.one/v2/"+path, nil)
			req.Header.Set("Proxy-Authorization", basic("talon-shop-a:x")[len("Proxy-Authorization: "):])
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			return resp.Body.Close()
		}
		first := make(chan error, 1)
		go func() { first <- get("first") }()
		if err := w.waitForVendorCalls("api.talon.one", 1); err != nil {
			return err
		}
		// The waiting call's own connection takes 150ms to look up.
		w.network.mu.Lock()
		w.network.lookupTime = 150 * time.Millisecond
		w.network.mu.Unlock()
		second := make(chan error, 1)
		go func() { second <- get("second") }()
		time.Sleep(50 * time.Millisecond)
		close(w.freed)
		return errors.Join(<-first, <-second)
	})
	sc.Step(`^the waiting call's trace says hotline.connection.reused true, with no "dns lookup", "tcp connect" or "vendor tls handshake" span$`, func() error {
		for _, r := range w.records() {
			t := w.traceOf(r)
			if vendor, err := t.one("vendor call"); err == nil && vendor.attrs["hotline.connection.reused"] == true {
				return t.hasNone("dns lookup", "tcp connect", "vendor tls handshake")
			}
		}
		return failf("no call reused a connection")
	})
	sc.Step(`^a call reuses it$`, func() error { return w.defaultCall() })

	sc.Step(`^a vendor that sends half of its reply, and the rest (\d+) milliseconds later$`, func(after int) error {
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(rw, strings.Repeat("a", 50))
			rw.(http.Flusher).Flush()
			time.Sleep(time.Duration(after) * time.Millisecond)
			_, _ = io.WriteString(rw, strings.Repeat("a", 50))
		})
		return nil
	})
	sc.Step(`^a call where the app's body breaks off$`, func() error {
		w.pending = &request{
			method: "PUT", target: w.target, headers: []string{basic(w.appID + ":x"), "Content-Type: application/json"},
			body: strings.NewReader(`{"half":`), length: 100, hangUp: true,
		}
		return nil
	})
	sc.Step(`^a call where (DNS fails|the TLS handshake fails|the vendor never answers)$`, func(failure string) error {
		return w.situation(map[string]string{
			"DNS fails":                "a vendor host that does not exist",
			"the TLS handshake fails":  "a TLS handshake that fails for a reason other than the certificate",
			"the vendor never answers": "a vendor slower than vendorTimeout",
		}[failure])
	})
	sc.Step(`^the "([^"]+)" span ends with status (error|unset|ok)$`, w.spanStatus)
	sc.Step(`^"vendor call" and "hotline call" end with status error$`, func() error {
		return errors.Join(w.spanStatus("vendor call", "error"), w.spanStatus("hotline call", "error"))
	})
	sc.Step(`^no span has an error.type$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			if err := hasNo(s.attrs, "error.type"); err != nil {
				return err
			}
		}
		return nil
	})

	sc.Step(`^a vendor whose IPv6 address cannot be reached and whose IPv4 address can$`, func() error {
		v := w.vendor("api.talon.one")
		v6 := netip.MustParseAddr("2001:db8::10")
		w.network.mu.Lock()
		w.network.names["api.talon.one"] = []netip.Addr{v6, v.ip}
		w.network.mu.Unlock()
		w.network.setUnreachable("[2001:db8::10]:443")
		return nil
	})
	sc.Step(`^a vendor whose first address never answers, with a vendorTimeout of 10 seconds$`, func() error {
		w.clock = &fakeClock{}
		w.network.stuck = w.clock.passToNext
		w.addIntegration(integration("talon-shop-a", w.talonAPI("    vendorTimeout: 10s\n")))
		v := w.vendor("api.talon.one")
		dead := netip.MustParseAddr("192.0.2.77")
		w.network.mu.Lock()
		w.network.names["api.talon.one"] = []netip.Addr{dead, v.ip}
		w.network.mu.Unlock()
		w.network.setHang("192.0.2.77:443")
		return nil
	})
	sc.Step(`^the call goes to its second address$`, func() error {
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("the app got %v", w.resp)
		}
		return w.lastHas("network.peer.address", w.vendor("api.talon.one").ip.String())
	})
	sc.Step(`^"tcp connect" lasts just over 5 seconds, the first address's share of the wait$`, func() error {
		return w.lasts("tcp connect", 5*time.Second, 6*time.Second)
	})
	sc.Step(`^the call goes to the IPv4 address$`, func() error {
		return w.lastHas("network.peer.address", w.vendor("api.talon.one").ip.String())
	})
	sc.Step(`^there is one "tcp connect" span, from the first try until connected$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		connect, err := t.one("tcp connect")
		if err != nil {
			return err
		}
		if call := t.call(); connect.end <= connect.start || connect.end-connect.start > call.end-call.start {
			return failf("tcp connect lasts %v, want above 0 and within the call", time.Duration(connect.end-connect.start))
		}
		return nil
	})

	sc.Step(`^"hotline call" has the record's trace_id and span_id$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		if call := t.call(); call.traceID != t.record.traceID || call.spanID != t.record.spanID {
			return failf("the call span has %s and %s, the record %s and %s", call.traceID, call.spanID, t.record.traceID, t.record.spanID)
		}
		return nil
	})
	sc.Step(`^every span has the record's trace_id$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			if s.traceID != t.record.traceID {
				return failf("%q has the trace_id %s, the record %s", s.name, s.traceID, t.record.traceID)
			}
		}
		return nil
	})
	sc.Step(`^"hotline call" has the trace_id (\w+) and the parent (\w+)$`, func(traceID, parent string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		if call := t.call(); call.traceID != traceID || call.parent != parent {
			return failf("the call span has the trace_id %s and the parent %q", call.traceID, call.parent)
		}
		return nil
	})
	sc.Step(`^"hotline call" has no parent$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		if p := t.call().parent; p != "" {
			return failf("the call span has the parent %s", p)
		}
		return nil
	})

	// The span flags OTLP defines: the parent's remoteness is known, and
	// the parent is remote.
	const hasIsRemote, isRemote = 0x100, 0x200
	remote := func(s receivedSpan) (bool, error) {
		if s.flags&hasIsRemote == 0 {
			return false, failf("%q does not say whether its parent is remote; its flags are %#x", s.name, s.flags)
		}
		return s.flags&isRemote != 0, nil
	}
	sc.Step(`^"hotline call" says its parent is (remote|not remote)$`, func(want string) error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		got, err := remote(t.call())
		if err != nil {
			return err
		}
		if got != (want == "remote") {
			return failf("the call span says its parent is remote: %v", got)
		}
		return nil
	})
	sc.Step(`^every other span says its parent is not remote$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			if s.name == "hotline call" {
				continue
			}
			got, err := remote(s)
			if err != nil {
				return err
			}
			if got {
				return failf("%q says its parent is remote", s.name)
			}
		}
		return nil
	})

	sc.Step(`^the receiver gets the spans at /v1/traces, with the exporter's resource labels$`, func() error {
		t, err := w.lastTrace()
		if err != nil {
			return err
		}
		for _, s := range t.spans {
			if s.resource["service.name"] != "hotline-proxy" || s.resource["service.instance.id"] != "proxy-test-1" {
				return failf("%q has the resource %v", s.name, s.resource)
			}
		}
		for _, seen := range w.receivers[0].requests() {
			if seen.path == "/v1/traces" && seen.contentType == "application/x-protobuf" {
				return nil
			}
		}
		return failf("no export came to /v1/traces; the receiver got %v", w.receivers[0].requests())
	})
	sc.Step(`^a second exporter$`, func() error {
		w.receivers = append(w.receivers, newReceiver())
		return nil
	})
	sc.Step(`^both receivers get the same spans$`, func() error {
		w.settle()
		ids := func(r *receiver) string {
			var list []string
			for _, s := range r.spans() {
				list = append(list, s.spanID+" "+s.name)
			}
			sort.Strings(list)
			return strings.Join(list, ", ")
		}
		first, second := ids(w.receivers[0]), ids(w.receivers[1])
		if first == "" || first != second {
			return failf("the receivers got %q and %q", first, second)
		}
		return nil
	})

	sc.Step(`^the (first|second) call's trace has (?:an|no) "app tls handshake" span(?: at the start of "hotline call")?$`, func(which string) error {
		if len(w.sentRecords) != 2 {
			return failf("got %d records, want two", len(w.sentRecords))
		}
		w.settle()
		t := w.traceOf(w.sentRecords[map[string]int{"first": 0, "second": 1}[which]])
		if which == "second" {
			return t.hasNone("app tls handshake")
		}
		handshake, err := t.one("app tls handshake")
		if err != nil {
			return err
		}
		call := t.call()
		if handshake.parent != call.spanID || handshake.start != call.start || handshake.end <= handshake.start {
			return failf("the handshake runs from %d to %d under %s; the call span %s starts at %d",
				handshake.start, handshake.end, handshake.parent, call.spanID, call.start)
		}
		return nil
	})
}

// callTrace is one call's spans, found from its record: the call span has
// the record's span id, and the rest sit under it.
type callTrace struct {
	record receivedRecord
	spans  []receivedSpan
}

func (t callTrace) call() receivedSpan {
	for _, s := range t.spans {
		if s.spanID == t.record.spanID {
			return s
		}
	}
	return receivedSpan{}
}

// one is the only span with the name.
func (t callTrace) one(name string) (receivedSpan, error) {
	var found []receivedSpan
	for _, s := range t.spans {
		if s.name == name {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return receivedSpan{}, failf("the trace has %d spans named %q, want one", len(found), name)
	}
	return found[0], nil
}

// children are the spans right under p, by start time.
func (t callTrace) children(p receivedSpan) []receivedSpan {
	var list []receivedSpan
	for _, s := range t.spans {
		if s.parent == p.spanID && s.traceID == p.traceID {
			list = append(list, s)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].start < list[j].start })
	return list
}

func (t callTrace) hasNone(names ...string) error {
	for _, s := range t.spans {
		for _, name := range names {
			if s.name == name {
				return failf("the trace has a %q span", name)
			}
		}
	}
	return nil
}

// traceOf is the trace of one record, once the proxy has sent everything.
func (w *world) traceOf(r receivedRecord) callTrace {
	all := w.receivers[0].spans()
	t := callTrace{record: r}
	in := map[string]bool{r.spanID: true}
	for grew := true; grew; {
		grew = false
		for _, s := range all {
			if s.traceID == r.traceID && !in[s.spanID] && (s.spanID == r.spanID || in[s.parent]) {
				in[s.spanID], grew = true, true
			}
		}
	}
	for _, s := range all {
		if s.traceID == r.traceID && in[s.spanID] {
			t.spans = append(t.spans, s)
		}
	}
	return t
}

// lastTrace is the trace of the last record.
func (w *world) lastTrace() (callTrace, error) {
	records := w.records()
	if len(records) == 0 {
		return callTrace{}, failf("no record arrived; the app got %v %q", w.resp, w.respBody)
	}
	t := w.traceOf(records[len(records)-1])
	if len(t.spans) == 0 {
		return t, failf("the record's trace has no spans; the receiver got %d spans", len(w.receivers[0].spans()))
	}
	return t, nil
}

// lasts checks that the span with the name lasts from least to most.
func (w *world) lasts(name string, least, most time.Duration) error {
	t, err := w.lastTrace()
	if err != nil {
		return err
	}
	s, err := t.one(name)
	if err != nil {
		return err
	}
	if d := time.Duration(s.end - s.start); d < least || d > most {
		return failf("%q lasts %v, want between %v and %v", name, d, least, most)
	}
	return nil
}

func (w *world) spanStatus(name, status string) error {
	t, err := w.lastTrace()
	if err != nil {
		return err
	}
	s, err := t.one(name)
	if err != nil {
		return err
	}
	if s.status != status {
		return failf("%q has the status %s, want %s", name, s.status, status)
	}
	return nil
}

func (w *world) lastRecord() (map[string]any, error) {
	records := w.records()
	if len(records) == 0 {
		return nil, failf("no record arrived; the app got %v %q", w.resp, w.respBody)
	}
	return records[len(records)-1].attrs, nil
}

func (w *world) lastHas(key string, value any) error {
	record, err := w.lastRecord()
	if err != nil {
		return err
	}
	return has(record, key, value)
}

func (w *world) lastHasAll(keys ...string) error {
	record, err := w.lastRecord()
	if err != nil {
		return err
	}
	return hasAll(record, keys...)
}

// secondsOf reads a timing, recorded in whole microseconds, as seconds.
func secondsOf(v any) (float64, bool) {
	us, ok := v.(int64)
	return float64(us) / 1e6, ok
}
