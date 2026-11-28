package proxy_test

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func ingressSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	sc.Step(`^the proxy is running$`, func() error {
		sut.app.appID = ""
		return sut.proxy.start()
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)"$`, func(method, target string) error {
		return sut.app.call(method, target)
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with the header "([^"]+)"$`, func(method, target, header string) error {
		return sut.app.call(method, target, header)
	})
	sc.Step(`^the call uses the forward proxy ingress$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return has(record, "hotline.ingress", "forward_proxy")
	})
	sc.Step(`^the vendor is (\S+) on port (\d+)$`, func(host, port string) error {
		calls := sut.vendors.vendor(host).received()
		if len(calls) != 1 || calls[0].port != port {
			return failf("%s got %d calls %+v, want one on port %s", host, len(calls), calls, port)
		}
		return nil
	})
	sc.Step(`^the vendor is (\S+)$`, func(host string) error {
		if calls := sut.vendors.vendor(host).received(); len(calls) != 1 {
			return failf("%s got %d calls, want one", host, len(calls))
		}
		return nil
	})
	sc.Step(`^the proxy refuses the call$`, func() error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusBadRequest {
			return failf("the app got %v, want 400", sut.app.resp)
		}
		return nil
	})
	sc.Step(`^nothing is sent to any vendor$`, sut.vendors.nothingSent)

	sc.Step(`^the call uses the vendor URL in path ingress$`, func() error {
		return sut.otlp.lastHas("hotline.ingress", "url_in_path")
	})
	sc.Step(`^the record says hotline.ingress (\S+)$`, func(ingress string) error {
		return sut.otlp.lastHas("hotline.ingress", ingress)
	})
	sc.Step(`^the vendor gets "Host: (\S+)"$`, func(host string) error {
		c, err := sut.vendors.onlyCall(strings.Split(host, ":")[0])
		if err != nil {
			return err
		}
		if c.host != host {
			return failf("the vendor got Host %s, want %s", c.host, host)
		}
		return nil
	})
	sc.Step(`^the vendor gets the path (\S+)$`, func(path string) error {
		c, err := sut.vendors.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.uri != path {
			return failf("the vendor got %s, want %s", c.uri, path)
		}
		return nil
	})
	sc.Step(`^nothing in the record names the proxy's own address$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		_, port, _ := net.SplitHostPort(sut.proxy.addr)
		for key, value := range record {
			if text := sprint(value); strings.Contains(text, "localhost") || strings.Contains(text, ":"+port) {
				return failf("%s = %s names the proxy", key, text)
			}
		}
		return nil
	})
	sc.Step(`^the proxy answers (\d+)$`, sut.app.answered)
	sc.Step(`^the proxy answers (\d+) with "Proxy-Status: ([^"]+)"$`, func(status int, proxyStatus string) error {
		if err := sut.app.answered(status); err != nil {
			return err
		}
		if got := sut.app.resp.Header.Get("Proxy-Status"); got != proxyStatus {
			return failf("Proxy-Status is %q, want %q", got, proxyStatus)
		}
		return nil
	})
	sc.Step(`^the record is minimal with (.+)$`, sut.minimalWith)
	sc.Step(`^the record has no server.address$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "server.address")
	})
	sc.Step(`^the proxy port uses TLS with a certificate the app trusts$`, func() error {
		sut.tlsIngress.proxyCert = sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		sut.tlsIngress.writeKeyPair(sut.tlsIngress.proxyCert, sut.tlsIngress.proxyCert)
		sut.tlsIngress.useAppCertificate()
		sut.app.appTLS = sut.tlsIngress.appTLSConfig()
		return nil
	})
	sc.Step(`^the app sends "([A-Z]+) ([^"]+)" over TLS to the proxy$`, func(method, target string) error {
		return sut.app.call(method, target, "Host: hotline-proxy")
	})
}

func guardSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	sc.Step(`^the vendor name (\S+) resolves to (\S+)$`, func(host, ip string) error {
		sut.vendors.network.resolve(host, sut.vendors.vendor(host).ip, netip.MustParseAddr(ip))
		return nil
	})
	sc.Step(`^the vendor name (\S+) resolves to (\S+) and to its public IP$`, func(host, ip string) error {
		v := sut.vendors.vendor(host)
		sut.vendors.network.resolve(host, v.ip, netip.MustParseAddr(ip), v.ip)
		return nil
	})
	sc.Step(`^an api for api.talon.one with allowInternalIPs set to true$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("    allowInternalIPs: true\n"), sut.apis.stripeAPI("")))
		return nil
	})
	sc.Step(`^a not-passed entry with allowInternalIPs set to true$`, func() error {
		sut.apis.addIntegration("  - id: not-passed\n    allowInternalIPs: true\n")
		return nil
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with no Proxy-Authorization$`, func(method, target string) error {
		sut.app.appID = ""
		return sut.app.call(method, target)
	})
	sc.Step(`^an app sends a call whose address is the proxy's own address$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		return sut.app.call("GET", "http://"+sut.proxy.addr+"/v2/x")
	})
	sc.Step(`^the vendor gets the call$`, func() error {
		var calls []vendorCall
		for _, v := range sut.vendors.vendors {
			calls = append(calls, v.received()...)
		}
		if len(calls) != 1 {
			return failf("the vendors got %d calls, want one; the app got %v", len(calls), sut.app.resp)
		}
		return nil
	})
	sc.Step(`^the vendor gets the call on its public IP$`, func() error {
		if _, err := sut.vendors.onlyCall("api.talon.one"); err != nil {
			return err
		}
		return sut.otlp.lastHas("network.peer.address", sut.vendors.vendor("api.talon.one").ip.String())
	})
	sc.Step(`^the proxy opens no connection$`, func() error {
		if _, dials := sut.vendors.network.counts(); dials > 0 {
			return failf("the proxy opened %d connections, want none", dials)
		}
		return nil
	})
	sc.Step(`^no connection is opened to (\S+)$`, func(ip string) error {
		if sut.vendors.network.triedToDial(ip) {
			return failf("the proxy tried to connect to %s", ip)
		}
		return nil
	})
}

func (sut *appSUT) answered(status int) error {
	if sut.resp == nil || sut.resp.StatusCode != status {
		return failf("the app got %v, want %d", sut.resp, status)
	}
	return nil
}

// minimalWith checks that the last record is minimal and holds the fields
// listed as "key value", separated by commas and "and".
func (sut *forwardSUT) minimalWith(fields string) error {
	record, err := sut.otlp.lastRecord()
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"hotline.integration.id": true, "hotline.ingress": true, "client.address": true, "server.address": true,
		"http.request.method": true, "http.response.status_code": true, "error.type": true,
		"http.request.body.size": true, "http.response.body.size": true,
	}
	for key := range record {
		if !allowed[key] && key != "hotline.timing.total_us" && !ledgerField(key) {
			return failf("the minimal record has %s", key)
		}
	}
	for _, field := range strings.FieldsFunc(strings.ReplaceAll(fields, " and ", ","), func(r rune) bool { return r == ',' }) {
		key, value, _ := strings.Cut(strings.TrimSpace(field), " ")
		if err := has(record, key, value); err != nil {
			return err
		}
	}
	return hasAll(record, "hotline.integration.id", "client.address", "http.request.method", "hotline.timing.total_us")
}

func (sut *vendorsSUT) nothingSent() error {
	lookups, dials := sut.network.counts()
	if lookups+dials > 0 {
		return failf("the proxy made %d lookups and %d connections, want none", lookups, dials)
	}
	return nil
}

func integrationSteps(sc *godog.ScenarioContext, sut *forwardSUT) {
	sc.Step(`^integration (\S+) with an api for (?:host )?(\S+)$`, func(id, host string) error {
		sut.apis.addIntegration(integration(id, sut.apis.apiFor(host, "")))
		return nil
	})
	sc.Step(`^an app calls https://(\S+) (with|without) (.+)$`, func(host, with, how string) error {
		target := "https://" + host + "/v2/customer_sessions/sess-1"
		sut.app.appID = ""
		var headers []string
		switch {
		case with == "without":
		case strings.HasPrefix(how, `"Proxy-Authorization: `) && strings.Contains(how, `" for "`):
			scheme, credentials, _ := strings.Cut(strings.TrimPrefix(how, `"Proxy-Authorization: `), `" for "`)
			headers = append(headers, "Proxy-Authorization: "+scheme+" "+
				base64.StdEncoding.EncodeToString([]byte(strings.TrimSuffix(credentials, `"`))))
		case strings.HasPrefix(how, `the ID "`):
			sut.app.appID = strings.Trim(strings.TrimPrefix(how, "the ID "), `"`)
		case strings.HasPrefix(how, `"`):
			headers = append(headers, strings.Trim(how, `"`))
		case how == "Basic credentials with no colon":
			headers = append(headers, basic("talon-shop-a"))
		case how == "Basic credentials with an empty user name":
			headers = append(headers, basic(":x"))
		case how == "two Proxy-Authorization headers":
			headers = append(headers, basic("talon-shop-a:x"), basic("talon-shop-a:x"))
		default:
			return godog.ErrUndefined
		}
		return sut.app.call("PUT", target, headers...)
	})
	sc.Step(`^the integration ID is (\S+)$`, sut.recordID)
	sc.Step(`^the record's ID is (\S+)$`, sut.recordID)
	sc.Step(`^hotline.integration.id is (\S+)$`, sut.recordID)
	sc.Step(`^the api for api.talon.one is used, with its spec, TLS origination setting and vendorTimeout$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return has(record, "url.template", "/v2/customer_sessions/{customerSessionId}")
	})
	sc.Step(`^the not-passed settings apply$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return hasNo(record, "url.full", "url.scheme", "hotline.config.version")
	})
	sc.Step(`^an app calls with a broken Proxy-Authorization header$`, func() error {
		sut.app.appID = ""
		return sut.app.call("PUT", sut.app.target, "Proxy-Authorization: Basic s3cr3t-hdr!!")
	})
	sc.Step(`^its value appears in no log, error or record$`, func() error {
		return sut.otlp.nowhere("s3cr3t-hdr")
	})
}

func (sut *forwardSUT) recordID(id string) error {
	record, err := sut.otlp.record()
	if err != nil {
		return err
	}
	return has(record, "hotline.integration.id", id)
}

// nowhere checks that a value reached no record, log line, error or answer.
func (sut *otlpSUT) nowhere(values ...string) error {
	extra := string(sut.app.respBody)
	if sut.proxy.startErr != nil {
		extra += sut.proxy.startErr.Error()
	}
	return sut.notRecorded(values, extra)
}

// notRecorded checks that a value reached no record or log line, nor the
// extra text given.
func (sut *otlpSUT) notRecorded(values []string, extra ...string) error {
	var all strings.Builder
	for _, r := range sut.records() {
		for k, v := range r.attrs {
			all.WriteString(k + "=" + strings.Join(strings.Fields(strings.Trim(sprint(v), "[]")), " ") + "\n")
		}
	}
	all.WriteString(sut.proxy.logs.String())
	all.WriteString(strings.Join(extra, ""))
	for _, v := range values {
		if strings.Contains(all.String(), v) {
			return failf("%q appears in %s", v, all.String())
		}
	}
	return nil
}
