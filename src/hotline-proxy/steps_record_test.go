package proxy_test

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func recordSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the Talon.One sample call ends$`, func() error { return w.talonSample() })
	sc.Step(`^event.name is hotline.request$`, func() error {
		records := w.records()
		if len(records) != 1 || records[0].eventName != "hotline.request" {
			return failf("got records %+v, want one hotline.request", records)
		}
		return nil
	})
	sc.Step(`^hotline.ingress is forward_proxy$`, func() error { return w.lastHas("hotline.ingress", "forward_proxy") })
	sc.Step(`^http.request.method is PUT and http.response.status_code is 200$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		if err := has(record, "http.request.method", "PUT"); err != nil {
			return err
		}
		return has(record, "http.response.status_code", 200)
	})
	sc.Step(`^url.full holds the redacted address$`, func() error {
		return w.lastHas("url.full", "https://api.talon.one/v2/customer_sessions/REDACTED?dry=true")
	})
	sc.Step(`^server.address is api.talon.one and server.port is 443$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		if err := has(record, "server.address", "api.talon.one"); err != nil {
			return err
		}
		return has(record, "server.port", 443)
	})
	sc.Step(`^the allowed headers, the total timing, network and TLS fields are recorded$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		if err := has(record, "http.request.header.content-type", []string{"application/json"}); err != nil {
			return err
		}
		return hasAll(record, "http.response.header.x-request-id", "hotline.timing.total_us", "network.peer.address", "network.protocol.version", "tls.protocol.version", "tls.server.not_after",
			"http.request.body.size", "http.response.body.size")
	})
	sc.Step(`^hotline.client.cancelled and hotline.config.version are recorded$`, func() error {
		return w.lastHasAll("hotline.client.cancelled", "hotline.config.version")
	})
	sc.Step(`^there are no (\S+) or (\S+)$`, func(first, second string) error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, first, second)
	})
	sc.Step(`^there is no error.type$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "error.type")
	})

	sc.Step(`^the config and specs from proxy-example$`, w.useExample)
	sc.Step(`^the full example config from proxy-example$`, w.useExample)
	sc.Step(`^(http\.response\.header\.\S+) is (\S+)$`, func(key, value string) error {
		return w.lastHas(key, []string{value})
	})
	sc.Step(`^an app sends an http address$`, func() error { return w.call("GET", "http://api.talon.one/v2/x") })
	sc.Step(`^url.scheme is (\S+)$`, func(scheme string) error { return w.lastHas("url.scheme", scheme) })
	sc.Step(`^a call goes to a path the spec does not know$`, func() error {
		return w.call("PUT", "https://api.talon.one/v2/unknown/abc")
	})
	sc.Step(`^url.full and url.template are left out$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.template", "url.full")
	})

	sc.Step(`^a call whose Content-Length header says 100 but whose body is 80 bytes$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		w.pending = &request{
			method:  "PUT",
			target:  w.target,
			headers: []string{basic("talon-shop-a:x")},
			body:    strings.NewReader(strings.Repeat("b", 80)),
			length:  100,
			hangUp:  true,
		}
		return nil
	})
	sc.Step(`^http.response.body.size is (\d+)$`, func(size int) error { return w.lastHas("http.response.body.size", size) })
	sc.Step(`^the record has no body sizes$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "http.request.body.size", "http.response.body.size")
	})
	sc.Step(`^http.request.body.size is (\d+)$`, func(size int) error { return w.lastHas("http.request.body.size", size) })

	sc.Step(`^(a call without Proxy-Authorization|a call with the unknown ID talon-shop-x|a call from talon-shop-a to api.stripe.com, outside its apis)$`, func(call string) error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		switch call {
		case "a call without Proxy-Authorization":
			w.appID = ""
		case "a call with the unknown ID talon-shop-x":
			w.appID = "talon-shop-x"
		default:
			w.method, w.target = "GET", "https://api.stripe.com/v1/payment_intents"
		}
		return nil
	})
	sc.Step(`^a call without Proxy-Authorization, a (\d+) byte request body and a (\d+) byte reply$`, func(sent, reply int) error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		w.appID = ""
		w.vendor("api.talon.one").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = rw.Write([]byte(strings.Repeat("r", reply)))
		})
		w.pending = &request{method: "PUT", target: w.target, length: sent, body: strings.NewReader(strings.Repeat("q", sent))}
		return nil
	})
	sc.Step(`^the not-passed entry has urlFull (on|off)$`, func(state string) error {
		w.addIntegration("  - id: not-passed\n    urlFull: " + map[string]string{"on": "true", "off": "false"}[state] + "\n")
		return nil
	})
	sc.Step(`^integration (\S+) with an api for (\S+), with content full$`, func(id, host string) error {
		w.addIntegration(integration(id, w.apiFor(host, "")) + "    content: full\n")
		return nil
	})
	sc.Step(`^the not-passed entry has content full$`, func() error {
		w.addIntegration("  - id: not-passed\n    content: full\n")
		return nil
	})
	sc.Step(`^integration (\S+) with an api for (\S+), with urlFull (on|off)$`, func(id, host, state string) error {
		w.addIntegration(integration(id, w.apiFor(host, "")) + "    urlFull: " + map[string]string{"on": "true", "off": "false"}[state] + "\n")
		return nil
	})
	sc.Step(`^the app (\S+) calls (https://\S+?), outside its apis$`, func(id, target string) error {
		w.appID = id
		return w.call("GET", target)
	})
	sc.Step(`^a call with no integration ID goes to (https://\S+)$`, func(target string) error {
		w.appID = ""
		return w.call("GET", target)
	})
	sc.Step(`^there is no url.full$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.full")
	})
	sc.Step(`^client.address is the app's IP address$`, func() error {
		return w.lastHas("client.address", "127.0.0.1")
	})
	sc.Step(`^the record holds only hotline.integration.id, hotline.ingress, client.address, server.address, network.peer.address, http.request.method, http.response.status_code, http.request.body.size, http.response.body.size, error.type and hotline.timing.total_us$`, func() error {
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		allowed := map[string]bool{
			"hotline.integration.id": true, "hotline.ingress": true, "client.address": true, "server.address": true, "network.peer.address": true,
			"http.request.method": true, "http.response.status_code": true, "error.type": true,
			"http.request.body.size": true, "http.response.body.size": true,
		}
		for key := range record {
			if !allowed[key] && key != "hotline.timing.total_us" {
				return failf("the minimal record has %s", key)
			}
		}
		if err := hasAll(record, "hotline.integration.id", "hotline.ingress", "client.address", "server.address",
			"http.request.method", "http.response.status_code", "http.request.body.size", "http.response.body.size",
			"hotline.timing.total_us"); err != nil {
			return err
		}
		// The vendor IP is the one the proxy connected to.
		host := sprint(record["server.address"])
		return has(record, "network.peer.address", w.vendor(host).ip.String())
	})

	sc.Step(`^a call with Authorization, Proxy-Authorization, a cookie, an API key and a JSON body$`, func() error {
		w.useSpec(`openapi: 3.1.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: key, in: query, x-hotline-redact: false }
        - { name: X-Api-Key, in: header, x-hotline-redact: false }
components:
  securitySchemes:
    api_key: { type: apiKey, in: header, name: X-Api-Key }
    query_key: { type: apiKey, in: query, name: key }
`)
		w.vendor("api.x.com").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Set-Cookie", "session=SECRET-8")
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"card":"SECRET-7"}`))
		})
		body := `{"card":"SECRET-6"}`
		w.pending = &request{
			method: "GET",
			target: "https://api.x.com/v1/things?key=SECRET-5",
			headers: []string{
				basic("talon-shop-a:SECRET-2"),
				"Authorization: Bearer SECRET-1",
				"Cookie: sid=SECRET-3",
				"X-Api-Key: SECRET-4",
				"Content-Type: application/json",
			},
			body:   strings.NewReader(body),
			length: len(body),
		}
		return nil
	})
	sc.Step(`^none of those values and no byte of either body appears anywhere in the record$`, func() error {
		if len(w.records()) != 1 {
			return failf("got %d records, want one", len(w.records()))
		}
		return w.notRecorded([]string{"SECRET", "card"})
	})

	sc.Step(`^an OTLP receiver that takes 2 seconds to take each export$`, func() error {
		w.receivers[0].delay = 2 * time.Second
		return nil
	})
	sc.Step(`^an app makes a call$`, func() error {
		started := time.Now()
		err := w.defaultCall()
		w.appTook = time.Since(started)
		return err
	})
	sc.Step(`^the app gets the reply without waiting for the record$`, func() error {
		if w.resp == nil || w.appTook > time.Second {
			return failf("the app got %v after %v", w.resp, w.appTook)
		}
		return nil
	})
}

// talonSample is sample call 1 from proxy-example.
func (w *world) talonSample() error {
	body := `{"customerSession":{"profileId":"jo@shop.com","state":"open","cartItems":[{"sku":"A1","quantity":0,"price":9.5}]}}`
	return w.send(request{
		method: "PUT",
		target: "https://api.talon.one/v2/customer_sessions/sess-8812?dry=true",
		headers: []string{
			"Proxy-Authorization: Basic dGFsb24tc2hvcC1hOng=",
			"Authorization: ApiKey-v1 c2VjcmV0",
			"Content-Type: application/json",
		},
		body:   strings.NewReader(body),
		length: len(body),
	})
}

// useExample uses the config and specs from proxy-example, with only the
// addresses changed to local ones.
func (w *world) useExample() error {
	w.writeFile("specs/talon-one.openapi.yaml", w.exampleFile("specs/talon-one.openapi.yaml"))
	w.writeFile("specs/stripe.openapi.yaml", w.exampleFile("specs/stripe.openapi.yaml"))
	w.addr = freePort()
	config := w.exampleFile("hotline-proxy.yaml")
	for _, from := range []string{"tcp://:8080", "http://hotline-control-plane.hotline:4318", "http://otel-collector.observability:4318",
		"/etc/hotline/keys/talon-shop-a", "HOTLINE_KEY_TALON_SHOP_B"} {
		if !strings.Contains(config, from) {
			return failf("the example config no longer has %s", from)
		}
	}
	config = strings.Replace(config, "tcp://:8080", "tcp://"+w.addr, 1)
	config = strings.ReplaceAll(config, "http://hotline-control-plane.hotline:4318", w.receivers[0].endpoint)
	w.receivers = append(w.receivers, newReceiver())
	config = strings.ReplaceAll(config, "http://otel-collector.observability:4318", w.receivers[1].endpoint)
	// The keys live where the test can write them.
	config = strings.ReplaceAll(config, "/etc/hotline/keys/talon-shop-a", w.writeFile("keys/talon-shop-a", "example-key-a\n"))
	w.env = map[string]string{"HOTLINE_KEY_TALON_SHOP_B": "example-key-b"}
	w.rawConfig = config
	return nil
}
