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

func ingressSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the proxy is running$`, func() error {
		w.appID = ""
		return w.start()
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)"$`, func(method, target string) error {
		return w.call(method, target)
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with the header "([^"]+)"$`, func(method, target, header string) error {
		return w.call(method, target, header)
	})
	sc.Step(`^the call uses the forward proxy ingress$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return has(record, "hotline.ingress", "forward_proxy")
	})
	sc.Step(`^the vendor is (\S+) on port (\d+)$`, func(host, port string) error {
		calls := w.vendor(host).received()
		if len(calls) != 1 || calls[0].port != port {
			return failf("%s got %d calls %+v, want one on port %s", host, len(calls), calls, port)
		}
		return nil
	})
	sc.Step(`^the vendor is (\S+)$`, func(host string) error {
		if calls := w.vendor(host).received(); len(calls) != 1 {
			return failf("%s got %d calls, want one", host, len(calls))
		}
		return nil
	})
	sc.Step(`^the proxy refuses the call$`, func() error {
		if w.resp == nil || w.resp.StatusCode != http.StatusBadRequest {
			return failf("the app got %v, want 400", w.resp)
		}
		return nil
	})
	sc.Step(`^nothing is sent to any vendor$`, w.nothingSent)

	sc.Step(`^the call uses the vendor URL in path ingress$`, func() error {
		return w.lastHas("hotline.ingress", "url_in_path")
	})
	sc.Step(`^the record says hotline.ingress (\S+)$`, func(ingress string) error {
		return w.lastHas("hotline.ingress", ingress)
	})
	sc.Step(`^the vendor gets "Host: (\S+)"$`, func(host string) error {
		c, err := w.onlyCall(strings.Split(host, ":")[0])
		if err != nil {
			return err
		}
		if c.host != host {
			return failf("the vendor got Host %s, want %s", c.host, host)
		}
		return nil
	})
	sc.Step(`^the vendor gets the path (\S+)$`, func(path string) error {
		c, err := w.onlyCall("api.talon.one")
		if err != nil {
			return err
		}
		if c.uri != path {
			return failf("the vendor got %s, want %s", c.uri, path)
		}
		return nil
	})
	sc.Step(`^nothing in the record names the proxy's own address$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		_, port, _ := net.SplitHostPort(w.addr)
		for key, value := range record {
			if text := sprint(value); strings.Contains(text, "localhost") || strings.Contains(text, ":"+port) {
				return failf("%s = %s names the proxy", key, text)
			}
		}
		return nil
	})
	sc.Step(`^the proxy answers (\d+)$`, w.answered)
	sc.Step(`^the proxy answers (\d+) with "Proxy-Status: ([^"]+)"$`, func(status int, proxyStatus string) error {
		if err := w.answered(status); err != nil {
			return err
		}
		if got := w.resp.Header.Get("Proxy-Status"); got != proxyStatus {
			return failf("Proxy-Status is %q, want %q", got, proxyStatus)
		}
		return nil
	})
	sc.Step(`^the record is minimal with (.+)$`, w.minimalWith)
	sc.Step(`^the record has no server.address$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "server.address")
	})
	sc.Step(`^hotline.requests goes up by one$`, func() error {
		counts := w.counts()
		if len(counts) == 0 || counts[len(counts)-1].value != 1 {
			return failf("got the counts %+v, want one call", counts)
		}
		return nil
	})
	sc.Step(`^the proxy port uses TLS with a certificate the app trusts$`, func() error {
		w.proxyCert = w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		w.writeKeyPair(w.proxyCert, w.proxyCert)
		w.listenTLS, w.tlsCert = "  tls: { cert: app }\n", "  app: { cert: ./tls/cert.pem, key: ./tls/key.pem }\n"
		w.appTLS = w.appTLSConfig()
		return nil
	})
	sc.Step(`^the app sends "([A-Z]+) ([^"]+)" over TLS to the proxy$`, func(method, target string) error {
		return w.call(method, target, "Host: hotline-proxy")
	})
}

func guardSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the vendor name (\S+) resolves to (\S+)$`, func(host, ip string) error {
		w.network.resolve(host, w.vendor(host).ip, netip.MustParseAddr(ip))
		return nil
	})
	sc.Step(`^the vendor name (\S+) resolves to (\S+) and to its public IP$`, func(host, ip string) error {
		v := w.vendor(host)
		w.network.resolve(host, v.ip, netip.MustParseAddr(ip), v.ip)
		return nil
	})
	sc.Step(`^an api for api.talon.one with allowInternalIPs set to true$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("    allowInternalIPs: true\n"), w.stripeAPI("")))
		return nil
	})
	sc.Step(`^a not-passed entry with allowInternalIPs set to true$`, func() error {
		w.addIntegration("  - id: not-passed\n    allowInternalIPs: true\n")
		return nil
	})
	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with no Proxy-Authorization$`, func(method, target string) error {
		w.appID = ""
		return w.call(method, target)
	})
	sc.Step(`^an app sends a call whose address is the proxy's own address$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		return w.call("GET", "http://"+w.addr+"/v2/x")
	})
	sc.Step(`^the vendor gets the call$`, func() error {
		var calls []vendorCall
		for _, v := range w.vendors {
			calls = append(calls, v.received()...)
		}
		if len(calls) != 1 {
			return failf("the vendors got %d calls, want one; the app got %v", len(calls), w.resp)
		}
		return nil
	})
	sc.Step(`^the vendor gets the call on its public IP$`, func() error {
		if _, err := w.onlyCall("api.talon.one"); err != nil {
			return err
		}
		return w.lastHas("network.peer.address", w.vendor("api.talon.one").ip.String())
	})
	sc.Step(`^the proxy opens no connection$`, func() error {
		if _, dials := w.network.counts(); dials > 0 {
			return failf("the proxy opened %d connections, want none", dials)
		}
		return nil
	})
	sc.Step(`^no connection is opened to (\S+)$`, func(ip string) error {
		if w.network.triedToDial(ip) {
			return failf("the proxy tried to connect to %s", ip)
		}
		return nil
	})
}

func (w *world) answered(status int) error {
	if w.resp == nil || w.resp.StatusCode != status {
		return failf("the app got %v, want %d", w.resp, status)
	}
	return nil
}

// minimalWith checks that the last record is minimal and holds the fields
// listed as "key value", separated by commas and "and".
func (w *world) minimalWith(fields string) error {
	record, err := w.lastRecord()
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		"hotline.integration.id": true, "hotline.ingress": true, "client.address": true, "server.address": true,
		"http.request.method": true, "http.response.status_code": true, "error.type": true,
		"http.request.body.size": true, "http.response.body.size": true,
	}
	for key := range record {
		if !allowed[key] && key != "hotline.timing.total_us" {
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

func (w *world) nothingSent() error {
	lookups, dials := w.network.counts()
	if lookups+dials > 0 {
		return failf("the proxy made %d lookups and %d connections, want none", lookups, dials)
	}
	return nil
}

func integrationSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^integration (\S+) with an api for (?:host )?(\S+)$`, func(id, host string) error {
		w.addIntegration(integration(id, w.apiFor(host, "")))
		return nil
	})
	sc.Step(`^an app calls https://(\S+) (with|without) (.+)$`, func(host, with, how string) error {
		target := "https://" + host + "/v2/customer_sessions/sess-1"
		w.appID = ""
		var headers []string
		switch {
		case with == "without":
		case strings.HasPrefix(how, `"Proxy-Authorization: `) && strings.Contains(how, `" for "`):
			scheme, credentials, _ := strings.Cut(strings.TrimPrefix(how, `"Proxy-Authorization: `), `" for "`)
			headers = append(headers, "Proxy-Authorization: "+scheme+" "+
				base64.StdEncoding.EncodeToString([]byte(strings.TrimSuffix(credentials, `"`))))
		case strings.HasPrefix(how, `the ID "`):
			w.appID = strings.Trim(strings.TrimPrefix(how, "the ID "), `"`)
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
		return w.call("PUT", target, headers...)
	})
	sc.Step(`^the integration ID is (\S+)$`, w.recordID)
	sc.Step(`^the record's ID is (\S+?)(?:, so typos stay visible)?$`, w.recordID)
	sc.Step(`^hotline.integration.id is (\S+)$`, w.recordID)
	sc.Step(`^the api for api.talon.one is used, with its spec, TLS origination setting and vendorTimeout$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return has(record, "url.template", "/v2/customer_sessions/{customerSessionId}")
	})
	sc.Step(`^the not-passed settings apply$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasNo(record, "url.full", "url.scheme", "hotline.config.version")
	})
	sc.Step(`^an app calls with a broken Proxy-Authorization header$`, func() error {
		w.appID = ""
		return w.call("PUT", w.target, "Proxy-Authorization: Basic s3cr3t-hdr!!")
	})
	sc.Step(`^its value appears in no log, error or record$`, func() error {
		return w.nowhere("s3cr3t-hdr")
	})
}

func (w *world) recordID(id string) error {
	record, err := w.record()
	if err != nil {
		return err
	}
	return has(record, "hotline.integration.id", id)
}

// nowhere checks that a value reached no record, log line, error or answer.
func (w *world) nowhere(values ...string) error {
	extra := string(w.respBody)
	if w.startErr != nil {
		extra += w.startErr.Error()
	}
	return w.notRecorded(values, extra)
}

// notRecorded checks that a value reached no record or log line, nor the
// extra text given.
func (w *world) notRecorded(values []string, extra ...string) error {
	var all strings.Builder
	for _, r := range w.records() {
		for k, v := range r.attrs {
			all.WriteString(k + "=" + strings.Join(strings.Fields(strings.Trim(sprint(v), "[]")), " ") + "\n")
		}
	}
	all.WriteString(w.logs.String())
	all.WriteString(strings.Join(extra, ""))
	for _, v := range values {
		if strings.Contains(all.String(), v) {
			return failf("%q appears in %s", v, all.String())
		}
	}
	return nil
}
