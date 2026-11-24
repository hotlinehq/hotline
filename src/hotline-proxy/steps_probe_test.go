package proxy_test

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	proxy "hotline-proxy"
)

// probeScene is what the probe scenarios arrange before the proxy starts:
// the probe settings of the Talon.One api, or of two apis for it, and how
// many integrations call it.
type probeScene struct {
	w *world
	// probeSetting is the probe line of the Talon.One api, when the scenario sets one.
	probeSetting string
	// twoAPIs, when set, are the probe lines of two apis for Talon.One, each
	// called by its own integration.
	twoAPIs []string
	// integrations is how many integrations call Talon.One; one unless set.
	integrations int
	// plainHTTP calls the vendor over http, on port 80.
	plainHTTP bool
	// firstCallAt is when the first call of the scenario was made.
	firstCallAt time.Time
}

// probeIDs are the integrations a probe scenario may use.
func probeIDs() []string { return []string{"talon-shop-a", "talon-shop-b", "talon-shop-c"} }

// writeConfig writes the scenario's integrations and apis, unless a step did, and
// uses a test clock, so a wait of minutes passes at once.
func (s *probeScene) writeConfig() {
	w := s.w
	if w.clock == nil {
		w.clock = &fakeClock{}
	}
	extra := "    vendorTimeout: 10s\n"
	if s.plainHTTP {
		extra += "    tlsOrigination: false\n"
	}
	if len(w.integrations) == 0 {
		switch {
		case s.twoAPIs != nil:
			w.addIntegration(integration(probeIDs()[0], w.talonAPI(extra+s.twoAPIs[0])))
			w.addIntegration(integration(probeIDs()[1], w.talonAPI(extra+s.twoAPIs[1])))
		default:
			api := w.talonAPI(extra + s.probeSetting)
			for _, id := range probeIDs()[:max(s.integrations, 1)] {
				w.addIntegration(integration(id, api))
			}
		}
	}
	if s.firstCallAt.IsZero() {
		s.firstCallAt = w.clock.Now()
	}
}

// probeLine is a probe setting from a scenario as a line of the api.
func probeLine(setting string) string { return "    " + setting + "\n" }

// talonAddress is the Talon.One vendor's address on port.
func (w *world) talonAddress(port string) string {
	return net.JoinHostPort(w.vendor("api.talon.one").ip.String(), port)
}

// refuseOn makes the Talon.One vendor refuse connections on port, and accept
// sends them to it again.
func (w *world) refuseOn(port string) {
	w.refuse("api.talon.one", port)
}

func (w *world) acceptOn(port string) {
	v := w.vendor("api.talon.one")
	// The vendor's first server answers https on port 443.
	w.network.route(w.talonAddress(port), v.servers[0].Listener.Addr().String())
	// A connection the vendor closes after each answer is never reused,
	// so the next call connects again.
	v.setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Connection", "close")
		_, _ = rw.Write([]byte(`{"ok":true}`))
	})
}

// settleProbes waits, while the proxy runs, until nothing more happens: no
// new connection, wait or record for a while, so a probe made due has
// started and set its next wait before the clock moves on.
func (w *world) settleProbes() {
	state := func() string {
		lookups, dials := w.network.counts()
		return fmt.Sprint(lookups, dials, w.clock.waiting(), len(w.receivers[0].probeRecords()), len(w.receivers[0].got()))
	}
	last, since := state(), time.Now()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); pause(5 * time.Millisecond) {
		if now := state(); now != last {
			last, since = now, time.Now()
			continue
		}
		if time.Since(since) > 60*time.Millisecond {
			return
		}
	}
}

// failingCall makes one call that fails to connect, and waits for its record.
func (s *probeScene) failingCall(id string) error {
	w := s.w
	before := len(w.receivers[0].got())
	w.appID = id
	if err := w.defaultCall(); err != nil {
		return err
	}
	if w.resp == nil || w.resp.StatusCode != http.StatusBadGateway {
		return failf("the call got %v, want 502", w.resp)
	}
	if err := w.waitForRecords(before + 1); err != nil {
		return err
	}
	w.settleProbes()
	return nil
}

// probesOf are the probe records of endpoint, once the proxy has stopped.
func (w *world) probesOf(endpoint string) []receivedRecord {
	w.settle()
	host, port, _ := net.SplitHostPort(endpoint)
	var out []receivedRecord
	for _, r := range w.receivers[0].probeRecords() {
		if fmt.Sprint(r.attrs["server.address"]) == host && fmt.Sprint(r.attrs["server.port"]) == port {
			out = append(out, r)
		}
	}
	return out
}

// onlyProbe is the scenario's only probe record.
func (w *world) onlyProbe() (receivedRecord, error) {
	w.settle()
	probes := w.receivers[0].probeRecords()
	if len(probes) != 1 {
		return receivedRecord{}, failf("the receiver got %d probe records, want one", len(probes))
	}
	return probes[0], nil
}

// probeSituation sets up a way a vendor call fails or works that only the
// probe scenarios need, as world.situation does for the others.
func (w *world) probeSituation(situation string) error {
	w.clock = &fakeClock{}
	w.addIntegration(integration("talon-shop-a", w.talonAPI("    vendorTimeout: 10s\n")))
	v := w.vendor("api.talon.one")
	switch situation {
	case "a vendor with a self-signed certificate":
		w.replaceTLS("api.talon.one", w.authority.leaf("api.talon.one", time.Now().Add(time.Hour), true))
	case "a vendor host that resolves only to internal IPs":
		w.network.resolve("api.talon.one", v.ip, netip.MustParseAddr("10.0.0.5"))
	case "a vendor that answers 500":
		v.setAnswer(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusInternalServerError) })
	case "a vendor that cuts the connection as TLS starts":
		w.listen(func(c net.Conn) { _ = c.Close() })
	case "a vendor that refused one call and now accepts connections":
		w.network.refuseNext(w.talonAddress("443"))
	case "a vendor that refused one call and whose name now resolves only to an internal IP":
		w.network.refuseNext(w.talonAddress("443"))
		w.network.namesAfterLookup["api.talon.one"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	case "a vendor that refused one call and then never answers, while no time passes":
		w.network.refuseNext(w.talonAddress("443"))
		w.network.setHang(w.talonAddress("443"))
		w.network.stuck = func() {}
	case "a vendor that never answers the TLS handshake":
		// It keeps letting time pass while the proxy waits, so every wait
		// for it runs out, whenever it started.
		w.listen(func(c net.Conn) {
			for {
				_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
				_, err := c.Read(make([]byte, 1024))
				switch {
				case isTimeout(err):
					w.stuck()
				case err != nil:
					return
				}
			}
		})
	case "a vendor called by its IP address that refuses the connection":
		w.refuseOn("443")
		w.target = "https://" + v.ip.String() + "/v2/customer_sessions/1"
	}
	return nil
}

// probeAnswers checks a probe record's three answers.
func probeAnswers(r receivedRecord, name, port, tls string) error {
	for question, want := range map[string]string{"name_resolves": name, "port_accepts_us": port, "tls_works": tls} {
		if err := has(r.attrs, "hotline.probe."+question+".answer", want); err != nil {
			return err
		}
		if _, found := r.attrs["hotline.probe."+question+".evidence"].([]any); !found {
			return failf("hotline.probe.%s.evidence is %v, want a list of lines", question, r.attrs["hotline.probe."+question+".evidence"])
		}
	}
	return nil
}

// minutes reads a list such as "0, 1, 3 and 7".
func minutes(list string) []int {
	var out []int
	for _, part := range strings.FieldsFunc(strings.ReplaceAll(list, " and ", ","), func(r rune) bool { return r == ',' }) {
		n, _ := strconv.Atoi(strings.TrimSpace(part))
		out = append(out, n)
	}
	return out
}

// spansOfTrace are the spans of one trace, by name.
func (w *world) spansOfTrace(traceID string) map[string]receivedSpan {
	out := map[string]receivedSpan{}
	for _, s := range w.receivers[0].spans() {
		if s.traceID == traceID {
			out[s.name] = s
		}
	}
	return out
}

// callSpanOf is the call span of a call's record, as a link names it.
func (w *world) callSpanOf(r receivedRecord) string { return r.traceID + "/" + r.spanID }

func probeSteps(sc *godog.ScenarioContext, w *world) {
	s := &probeScene{w: w}
	sc.Step(`^(a vendor with a self-signed certificate|a vendor host that resolves only to internal IPs|a vendor that answers 500|a vendor that cuts the connection as TLS starts|a vendor that refused one call and now accepts connections|a vendor that refused one call and whose name now resolves only to an internal IP|a vendor called by its IP address that refuses the connection|a vendor that never answers the TLS handshake|a vendor that refused one call and then never answers, while no time passes)$`, w.probeSituation)
	sc.Step(`^a vendor that refused one call over plain HTTP and now accepts connections$`, func() error {
		s.plainHTTP = true
		w.target = "http://api.talon.one/v2/customer_sessions/1"
		w.network.refuseNext(w.talonAddress("80"))
		return nil
	})
	sc.Step(`^a vendor host whose name resolves to a private IP and to a public IP that refuses the connection$`, func() error {
		v := w.vendor("api.talon.one")
		w.refuseOn("443")
		w.network.resolve("api.talon.one", v.ip, netip.MustParseAddr("10.1.2.3"), v.ip)
		return nil
	})
	sc.Step(`^the vendor refuses connections$`, func() error {
		w.refuseOn("443")
		return nil
	})
	sc.Step(`^the vendor refuses connections on ports 443 and 8443$`, func() error {
		w.refuseOn("443")
		w.refuseOn("8443")
		return nil
	})
	sc.Step(`^the api for "api.talon.one" has "([^"]+)"$`, func(setting string) error {
		s.probeSetting = probeLine(setting)
		return nil
	})
	sc.Step(`^two apis for "api.talon.one", one with "([^"]+)" and one with no probe settings$`, func(setting string) error {
		s.twoAPIs = []string{probeLine(setting), ""}
		return nil
	})
	sc.Step(`^the integration talon-shop-a has "([^"]+)"$`, func(setting string) error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")) + "    " + setting + "\n")
		return nil
	})

	sc.Step(`^an app calls the vendor through the proxy$`, func() error {
		s.writeConfig()
		return w.defaultCall()
	})
	sc.Step(`^an app with no integration ID calls the vendor through the proxy$`, func() error {
		s.writeConfig()
		w.appID = ""
		return w.defaultCall()
	})
	sc.Step(`^the proxy starts with it$`, func() error {
		s.writeConfig()
		_ = w.start()
		return nil
	})
	sc.Step(`^(\d+) calls from three integrations fail within one second$`, func(n int) error {
		s.integrations = 3
		s.writeConfig()
		for i := range n {
			if err := s.failingCall(probeIDs()[i%3]); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^an app calls each port through the proxy$`, func() error {
		s.writeConfig()
		if err := w.defaultCall(); err != nil {
			return err
		}
		w.target = "https://api.talon.one:8443/v2/customer_sessions/1"
		return w.defaultCall()
	})
	everyMinute := func(n int) error {
		s.writeConfig()
		for range n {
			if s.twoAPIs != nil {
				if err := s.failingCall(probeIDs()[1]); err != nil {
					return err
				}
			}
			if err := s.failingCall(probeIDs()[0]); err != nil {
				return err
			}
			w.clock.pass(time.Minute)
			w.settleProbes()
		}
		return nil
	}
	sc.Step(`^a call to it fails every minute for (\d+) minutes?(?: more)?$`, everyMinute)
	sc.Step(`^a call through each api fails every minute for (\d+) minutes$`, everyMinute)
	sc.Step(`^the vendor accepts connections again and a call succeeds$`, func() error {
		w.acceptOn("443")
		w.appID = probeIDs()[0]
		if err := w.defaultCall(); err != nil {
			return err
		}
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("the call got %v, want 200", w.resp)
		}
		w.settleProbes()
		return nil
	})
	sc.Step(`^another call to it succeeds$`, func() error {
		if err := w.defaultCall(); err != nil {
			return err
		}
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("the call got %v, want 200", w.resp)
		}
		w.settleProbes()
		return nil
	})
	sc.Step(`^a call to it fails again 1 minute later$`, func() error {
		w.clock.pass(time.Minute)
		w.settleProbes()
		w.refuseOn("443")
		return s.failingCall(probeIDs()[0])
	})
	sc.Step(`^no call to it fails for (\d+) minutes$`, func(n int) error {
		for range n {
			w.clock.pass(time.Minute)
			w.settleProbes()
		}
		return nil
	})
	sc.Step(`^a call to it fails after that$`, func() error { return s.failingCall(probeIDs()[0]) })
	sc.Step(`^the config is reloaded with "([^"]+)" on the api$`, func(setting string) error {
		for i := range w.apis {
			w.apis[i].yaml += probeLine(setting)
		}
		before := strings.Count(w.logs.String(), "config reloaded")
		if err := w.reloadConfig(); err != nil {
			return err
		}
		if strings.Count(w.logs.String(), "config reloaded") <= before {
			return failf("the config was not reloaded: %s", w.logs.String())
		}
		return nil
	})

	sc.Step(`^one probe of "([^"]+)" is exported$`, func(endpoint string) error {
		if got := w.probesOf(endpoint); len(got) != 1 {
			return failf("the receiver got %d probes of %s, want one; it got %v", len(got), endpoint, w.receivers[0].probeRecords())
		}
		return nil
	})
	sc.Step(`^no probe is exported$`, func() error {
		w.settle()
		if got := w.receivers[0].probeRecords(); len(got) != 0 {
			return failf("the receiver got %d probes, want none: %v", len(got), got[0].attrs)
		}
		return nil
	})
	sc.Step(`^the proxy exports exactly one call record$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d call records, want one", got)
		}
		return nil
	})
	sc.Step(`^probes of "([^"]+)" start at minutes (.+)$`, func(endpoint, list string) error {
		var got []int
		for _, r := range w.probesOf(endpoint) {
			at := time.Unix(0, int64(r.timestamp)) //nolint:gosec // nanoseconds since 1970 fit until 2262
			got = append(got, int(math.Round(at.Sub(s.firstCallAt).Minutes())))
		}
		slices.Sort(got)
		if want := minutes(list); !slices.Equal(got, want) {
			return failf("probes started at minutes %v, want %v", got, want)
		}
		return nil
	})

	sc.Step(`^the probe's record has hotline.probe.side (\S+) and hotline.probe.verdict (\S+)$`, func(side, verdict string) error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if err := has(r.attrs, "hotline.probe.side", side); err != nil {
			return err
		}
		return has(r.attrs, "hotline.probe.verdict", verdict)
	})
	sc.Step(`^its answers are name_resolves (\S+), port_accepts_us (\S+) and tls_works (\S+)$`, func(name, port, tls string) error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		return probeAnswers(r, name, port, tls)
	})
	sc.Step(`^the proxy never tried to connect to the private IP$`, func() error {
		if w.network.triedToDial("10.1.2.3") {
			return failf("the proxy connected to 10.1.2.3")
		}
		return nil
	})

	sc.Step(`^the probe's log record has event name "hotline.probe" and severity WARN$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if r.eventName != "hotline.probe" || r.severityText != "WARN" {
			return failf("the probe record is %s at %s", r.eventName, r.severityText)
		}
		return nil
	})
	sc.Step(`^its body names "api.talon.one:443" and says it refused the connection$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if !strings.Contains(r.body, "api.talon.one:443") || !strings.Contains(r.body, "refused the connection") {
			return failf("the body is %q", r.body)
		}
		return nil
	})
	sc.Step(`^it has server.address "api.talon.one" and server.port 443$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if err := has(r.attrs, "server.address", "api.talon.one"); err != nil {
			return err
		}
		return has(r.attrs, "server.port", 443)
	})
	sc.Step(`^its hotline.probe.port_accepts_us.evidence names the vendor's IP and a reset$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		evidence := fmt.Sprint(r.attrs["hotline.probe.port_accepts_us.evidence"])
		if !strings.Contains(evidence, w.talonAddress("443")) || !strings.Contains(evidence, "reset") {
			return failf("the evidence is %s", evidence)
		}
		return nil
	})
	sc.Step(`^it carries the trace id and span id of the probe's root span$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		root, found := w.spansOfTrace(r.traceID)["probe"]
		if !found || root.spanID != r.spanID || root.parent != "" {
			return failf("the record's trace %s and span %s are not the root span of a probe trace", r.traceID, r.spanID)
		}
		return nil
	})
	sc.Step(`^it has a hotline.record.hash in the ledger chain "hotline.probe"$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if err := hasAll(r.attrs, "hotline.record.hash", "hotline.trace.hash"); err != nil {
			return err
		}
		for _, b := range w.receivers[0].ledger() {
			if b.attrs["hotline.batch.id"] == r.attrs["hotline.batch.id"] {
				return has(b.attrs, "hotline.batch.integration", "hotline.probe")
			}
		}
		return failf("no batch record has the probe's batch %v", r.attrs["hotline.batch.id"])
	})
	sc.Step(`^the probe's log record has severity INFO and hotline.probe.verdict reachable_now$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		if r.severityText != "INFO" {
			return failf("the probe record's severity is %s", r.severityText)
		}
		return has(r.attrs, "hotline.probe.verdict", "reachable_now")
	})

	sc.Step(`^the probe's trace has a root span "probe" with hotline.probe.side vendor_side$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		root, found := w.spansOfTrace(r.traceID)["probe"]
		if !found {
			return failf("the probe's trace has no span probe")
		}
		return has(root.attrs, "hotline.probe.side", "vendor_side")
	})
	sc.Step(`^the root span links to the call span of the failed call$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		calls := w.records()
		root := w.spansOfTrace(r.traceID)["probe"]
		if len(calls) != 1 || !slices.Equal(root.links, []string{w.callSpanOf(calls[0])}) {
			return failf("the root span links to %v, want the call span %v", root.links, calls)
		}
		return nil
	})
	sc.Step(`^under it are spans "name_resolves" and "port_accepts_us", in that order$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		spans := w.spansOfTrace(r.traceID)
		root := spans["probe"]
		name, port := spans["name_resolves"], spans["port_accepts_us"]
		if len(spans) != 3 || name.parent != root.spanID || port.parent != root.spanID || port.start < name.end {
			return failf("the probe's trace has %v", spans)
		}
		return nil
	})
	sc.Step(`^each has hotline.probe.question, hotline.probe.answer, hotline.probe.points_to and hotline.probe.evidence$`, func() error {
		r, err := w.onlyProbe()
		if err != nil {
			return err
		}
		for name, span := range w.spansOfTrace(r.traceID) {
			if name == "probe" {
				continue
			}
			if err := hasAll(span.attrs, "hotline.probe.question", "hotline.probe.answer", "hotline.probe.points_to", "hotline.probe.evidence"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the second probe's root span links to the call span of the last call that failed before it$`, func() error {
		probes := w.probesOf("api.talon.one:443")
		calls := w.records()
		if len(probes) != 2 || len(calls) != 2 {
			return failf("there are %d probes and %d calls, want two of each", len(probes), len(calls))
		}
		root := w.spansOfTrace(probes[1].traceID)["probe"]
		if !slices.Equal(root.links, []string{w.callSpanOf(calls[1])}) {
			return failf("the second probe links to %v, want the second call %s", root.links, w.callSpanOf(calls[1]))
		}
		return nil
	})
	sc.Step(`^the second call's record has hotline.probe.side vendor_side and hotline.probe.verdict refused_by_vendor$`, func() error {
		calls := w.records()
		if len(calls) != 2 {
			return failf("there are %d call records, want two", len(calls))
		}
		if err := has(calls[1].attrs, "hotline.probe.side", "vendor_side"); err != nil {
			return err
		}
		return has(calls[1].attrs, "hotline.probe.verdict", "refused_by_vendor")
	})
	linkedToFirstProbe := func(record receivedRecord) error {
		probes := w.probesOf("api.talon.one:443")
		if len(probes) == 0 {
			return failf("no probe was exported")
		}
		if err := has(record.attrs, "hotline.probe.record_hash", probes[0].attrs["hotline.record.hash"]); err != nil {
			return err
		}
		return has(record.attrs, "hotline.probe.trace_id", probes[0].traceID)
	}
	sc.Step(`^it has hotline.probe.record_hash and hotline.probe.trace_id of the first probe$`, func() error {
		return linkedToFirstProbe(w.records()[1])
	})
	sc.Step(`^it has hotline.probe.time, when that probe ended$`, func() error {
		probes := w.probesOf("api.talon.one:443")
		ended, err := time.Parse(time.RFC3339Nano, fmt.Sprint(w.records()[1].attrs["hotline.probe.time"]))
		if err != nil {
			return err
		}
		root := w.spansOfTrace(probes[0].traceID)["probe"]
		if ended.UnixNano() != root.end {
			return failf("hotline.probe.time is %v, want the probe span's end %v", ended, time.Unix(0, root.end))
		}
		return nil
	})
	sc.Step(`^that call's record has hotline.probe.record_hash and hotline.probe.trace_id of the first probe$`, func() error {
		calls := w.records()
		return linkedToFirstProbe(calls[len(calls)-1])
	})
	sc.Step(`^it has no hotline.probe.side and no hotline.probe.verdict$`, func() error {
		calls := w.records()
		return hasNo(calls[len(calls)-1].attrs, "hotline.probe.side", "hotline.probe.verdict", "hotline.probe.time")
	})
	noLink := func(record receivedRecord) error {
		for key := range record.attrs {
			if strings.HasPrefix(key, "hotline.probe.") {
				return failf("the record has %s", key)
			}
		}
		return nil
	}
	sc.Step(`^the first call's record has no hotline.probe attributes$`, func() error {
		calls := w.records()
		if len(calls) == 0 {
			return failf("no call record")
		}
		return noLink(calls[0])
	})
	sc.Step(`^the last call's record has no hotline.probe attributes$`, func() error {
		calls := w.records()
		return noLink(calls[len(calls)-1])
	})

	sc.Step(`^the file has a line with event "hotline.probe", its verdict sentence as body, and its root span's link$`, func() error {
		lines, err := w.allFileLines(0)
		if err != nil {
			return err
		}
		for _, line := range lines {
			if line["event"] != "hotline.probe" {
				continue
			}
			if !strings.Contains(fmt.Sprint(line["body"]), "refused the connection") {
				return failf("the probe's line has the body %v", line["body"])
			}
			spans, _ := line["attributes"].(map[string]any)["hotline.trace.spans"].([]any)
			if len(spans) == 0 || spans[0].(map[string]any)["links"] == nil {
				return failf("the probe's line has no root span with a link: %v", spans)
			}
			return nil
		}
		return failf("the file has no probe line: %v", lines)
	})
	sc.Step(`^the console prints a line with "PROBE", "api.talon.one:443", "vendor_side" and "refused the connection"$`, func() error {
		w.settle()
		for _, line := range strings.Split(w.console.String(), "\n") {
			if containsAll(line, []string{"PROBE", "api.talon.one:443", "vendor_side", "refused the connection"}) {
				return nil
			}
		}
		return failf("the console printed %q", w.console.String())
	})
	sc.Step(`^its probe is running$`, func() error {
		for deadline := time.Now().Add(waitLimit); time.Now().Before(deadline); pause(5 * time.Millisecond) {
			if _, dials := w.network.counts(); dials >= 2 {
				return nil
			}
		}
		return failf("no probe started")
	})
	sc.Step(`^the console prints the probe's line in green$`, func() error {
		return w.consoleHas("\x1b[1;32mPROBE\x1b[0m")
	})
	sc.Step(`^the third line is the probe of the vendor, with "([^"]+)"$`, func(text string) error {
		w.settle()
		lines := strings.Split(strings.TrimSuffix(w.console.String(), "\n"), "\n")
		if len(lines) != 3 || !strings.Contains(lines[2], text) {
			return failf("the console printed %q", w.console.String())
		}
		return nil
	})
	sc.Step(`^hotline-proxy verify finds every record and batch of the file sound$`, func() error {
		w.settle()
		result := w.verify(proxy.VerifyOptions{Folder: w.files[0].path})
		if result.code != 0 {
			return failf("verify gave %d: %s", result.code, result.out)
		}
		return nil
	})
}
