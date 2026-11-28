package proxy_test

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/cucumber/godog"
)

// pathSituation sets up the network between the proxy and a vendor whose
// port gives no answer or cannot be reached, router by router, for the
// path probes of slice 2.
func (sut *probeSUT) pathSituation(situation string) error {
	v := sut.vendors.vendor("api.talon.one")
	address := sut.talonAddress("443")
	ours, public2, public3 := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("198.51.100.2"), netip.MustParseAddr("198.51.100.3")
	routers := []netip.Addr{netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("203.0.113.3")}
	route := &path{routers: routers}
	sut.vendors.network.setHang(address)
	switch situation {
	case "a vendor whose firewall drops our packets, and whose machine answers UDP":
		route.answersUDP = true
	case "a vendor whose firewall drops our packets, and whose machine answers only ping":
		route.answersPing = true
	case "a vendor whose firewall drops our packets, where ping is not allowed":
		route.pingNotAllowed = true
	case "a path whose packets stop after our own router":
		route = &path{routers: []netip.Addr{ours, public2, public3}, diesAfter: 1}
	case "a path whose packets stop after a public router":
		route = &path{routers: []netip.Addr{ours, public2, public3}, diesAfter: 2}
	case "a path where no router answers":
		route = &path{}
	case `a firewall on the way that answers "administratively prohibited"`:
		sut.vendors.network.setUnreachable(address)
		route = &path{routers: []netip.Addr{ours, public2}, prohibitedBy: 2, answersUDP: true}
	case `a firewall of ours on the way that answers "administratively prohibited"`:
		sut.vendors.network.setUnreachable(address)
		route = &path{routers: []netip.Addr{ours}, prohibitedBy: 1}
	case "a router on the way that says the vendor's machine cannot be reached":
		sut.vendors.network.setUnreachable(address)
		route = &path{routers: []netip.Addr{ours, public2}, unreachableBy: 2}
	case "a vendor whose firewall drops our packets, where path probes cannot run":
		return nil
	}
	sut.vendors.network.mu.Lock()
	defer sut.vendors.network.mu.Unlock()
	sut.vendors.network.paths[v.ip] = route
	return nil
}

// spansIn are the spans of one trace, in the order they were sent.
func (sut *probeSUT) spansIn(traceID string) []receivedSpan {
	var out []receivedSpan
	for _, s := range sut.otlp.first().spans() {
		if s.traceID == traceID {
			out = append(out, s)
		}
	}
	return out
}

// hopSpans are the hop spans of a probe's trace, over one transport.
func (sut *probeSUT) hopSpans(r receivedRecord, transport string) []receivedSpan {
	var hops []receivedSpan
	path := sut.spansOfTrace(r.traceID)["path_reaches_vendor"]
	for _, s := range sut.spansIn(r.traceID) {
		if s.name == "hop" && s.parent == path.spanID && s.attrs["network.transport"] == transport {
			hops = append(hops, s)
		}
	}
	return hops
}

// hopTTLs are the TTLs of hop spans.
func hopTTLs(hops []receivedSpan) []int {
	ttls := make([]int, len(hops))
	for i, h := range hops {
		ttl, _ := h.attrs["hotline.probe.hop.ttl"].(int64)
		ttls[i] = int(ttl)
	}
	return ttls
}

// oneTo is the numbers from 1 to n.
func oneTo(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func pathSteps(sc *godog.ScenarioContext, sut *probeSUT) {
	sc.Step(`^(a vendor whose firewall drops our packets, and whose machine answers UDP|a vendor whose firewall drops our packets, and whose machine answers only ping|a vendor whose firewall drops our packets, where ping is not allowed|a path whose packets stop after our own router|a path whose packets stop after a public router|a path where no router answers|a firewall on the way that answers "administratively prohibited"|a firewall of ours on the way that answers "administratively prohibited"|a router on the way that says the vendor's machine cannot be reached|a vendor whose firewall drops our packets, where path probes cannot run)$`, sut.pathSituation)

	sc.Step(`^its path answers are path_reaches_vendor (\S+) and host_up (\S+)$`, func(path, host string) error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		for question, want := range map[string]string{"path_reaches_vendor": path, "host_up": host} {
			if err := has(r.attrs, "hotline.probe."+question+".answer", want); err != nil {
				return err
			}
			if _, found := r.attrs["hotline.probe."+question+".evidence"].([]any); !found {
				return failf("hotline.probe.%s.evidence is %v, want a list of lines", question, r.attrs["hotline.probe."+question+".evidence"])
			}
		}
		return nil
	})
	sc.Step(`^the probe's body says "([^"]*)"$`, func(text string) error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		if !strings.Contains(r.body, text) {
			return failf("the body is %q, want it to say %q", r.body, text)
		}
		return nil
	})
	sc.Step(`^the probe's trace has spans "name_resolves", "path_reaches_vendor", "host_up" and "port_accepts_us", in that order$`, func() error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		root := sut.spansOfTrace(r.traceID)["probe"]
		var names []string
		for _, s := range sut.spansIn(r.traceID) {
			if s.parent == root.spanID {
				names = append(names, s.name)
			}
		}
		if want := []string{"name_resolves", "path_reaches_vendor", "host_up", "port_accepts_us"}; !slices.Equal(names, want) {
			return failf("the probe's spans are %v, want %v", names, want)
		}
		return nil
	})
	sc.Step(`^the span "path_reaches_vendor" has "hop" spans under it for tcp with hotline.probe.hop.ttl 1 to (\d+), and for udp 1 to (\d+)$`, func(tcp, udp int) error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		for transport, last := range map[string]int{"tcp": tcp, "udp": udp} {
			if got := hopTTLs(sut.hopSpans(r, transport)); !slices.Equal(got, oneTo(last)) {
				return failf("the %s hop spans have TTLs %v, want 1 to %d", transport, got, last)
			}
		}
		return nil
	})
	sc.Step(`^the first hop span has hotline.probe.hop.from "([^"]+)", hotline.probe.hop.answer "([^"]+)" and network.transport "([^"]+)"$`, func(from, answer, transport string) error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		hops := sut.hopSpans(r, transport)
		if len(hops) == 0 {
			return failf("the probe's trace has no %s hop spans", transport)
		}
		if err := has(hops[0].attrs, "hotline.probe.hop.from", from); err != nil {
			return err
		}
		return has(hops[0].attrs, "hotline.probe.hop.answer", answer)
	})
	sc.Step(`^the probe's hotline.probe.path_reaches_vendor.evidence has a line "([^"]+)"$`, func(line string) error {
		r, err := sut.onlyProbe()
		if err != nil {
			return err
		}
		lines, _ := r.attrs["hotline.probe.path_reaches_vendor.evidence"].([]any)
		if !slices.Contains(lines, any(line)) {
			return failf("the evidence is %v, want a line %q", lines, line)
		}
		return nil
	})
	sc.Step(`^the proxy sent no path packets$`, func() error {
		sut.vendors.network.mu.Lock()
		defer sut.vendors.network.mu.Unlock()
		if len(sut.vendors.network.packets) > 0 {
			return failf("the proxy sent the path packets %v", sut.vendors.network.packets)
		}
		return nil
	})
	sc.Step(`^no TCP or UDP path packet crossed more than (\d+) routers$`, func(most int) error {
		// The probe runs after the call; its record says it has ended.
		if _, err := sut.onlyProbe(); err != nil {
			return err
		}
		sut.vendors.network.mu.Lock()
		defer sut.vendors.network.mu.Unlock()
		for _, p := range sut.vendors.network.packets {
			if p.Transport != "ping" && p.TTL > most {
				return failf("the proxy sent %s", fmt.Sprint(p))
			}
		}
		if len(sut.vendors.network.packets) == 0 {
			return failf("the proxy sent no path packets")
		}
		return nil
	})
}
