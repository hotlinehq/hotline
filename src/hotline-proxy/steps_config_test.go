package proxy_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func configSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a valid config file$`, func() error {
		w.receivers = append(w.receivers, newReceiver())
		return nil
	})
	sc.Step(`^the proxy (?:loads|reports) (?:it|the config|the problem)$`, func() error {
		w.startErr = nil
		_ = w.start()
		return nil
	})
	sc.Step(`^a valid config file named (\S+)$`, func(name string) error {
		w.configName = name
		return nil
	})
	sc.Step(`^every record carries hotline.config.version (\S+) and a fingerprint$`, func(version string) error {
		if err := w.defaultCall(); err != nil {
			return err
		}
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		got, _ := record["hotline.config.version"].(string)
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(version) + `\+[0-9a-f]{12}$`).MatchString(got) {
			return failf("hotline.config.version is %q, want %s+ and 12 hex characters", got, version)
		}
		return nil
	})
	sc.Step(`^the proxy listen address is read$`, func() error {
		if w.startErr != nil {
			return w.startErr
		}
		if err := w.defaultCall(); err != nil {
			return err
		}
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("a call to %s got %v", w.addr, w.resp)
		}
		return nil
	})
	sc.Step(`^every OTLP exporter is read with its endpoint$`, func() error {
		w.settle()
		for _, r := range w.receivers {
			if records, _ := r.got(); len(records) != 1 {
				return failf("the exporter at %s got %d records", r.endpoint, len(records))
			}
		}
		return nil
	})
	sc.Step(`^every api is read with its hosts and the spec it points to$`, func() error {
		return w.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1":   "/v2/customer_sessions/{customerSessionId}",
			"GET https://api.stripe.com/v1/payment_intents/pi_1": "/v1/payment_intents/{intent}",
		})
	})
	sc.Step(`^every integration is read with the apis it names$`, func() error {
		for _, r := range w.records() {
			if err := has(r.attrs, "hotline.integration.id", "talon-shop-a"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^spec files are found relative to the folder of the config file$`, func() error {
		if !strings.HasPrefix(w.configPath, w.dir) {
			return failf("the config file %s is not in the test folder", w.configPath)
		}
		return nil
	})
	sc.Step(`^it loads together with both of its specs$`, func() error {
		if w.startErr != nil {
			return w.startErr
		}
		return w.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1": "/v2/customer_sessions/{customerSessionId}",
			"GET https://api.stripe.com/v1/payment_intents":    "/v1/payment_intents",
		})
	})

	sc.Step(`^an api that (does not set tlsOrigination|sets tlsOrigination to false|does not set vendorTimeout|sets vendorTimeout to 10s)$`, func(sets string) error {
		extra := map[string]string{
			"sets tlsOrigination to false": "    tlsOrigination: false\n",
			"sets vendorTimeout to 10s":    "    vendorTimeout: 10s\n",
		}[sets]
		w.addIntegration(integration("talon-shop-a", w.talonAPI(extra)))
		return nil
	})
	sc.Step(`^TLS origination is (on|off)$`, func(on string) error {
		if err := w.call("GET", "http://api.talon.one/v2/x"); err != nil {
			return err
		}
		if on == "on" {
			return w.calledOver("https", "443")
		}
		return w.calledOver("http", "80")
	})
	sc.Step(`^the proxy waits (\d+) seconds for the vendor$`, w.waitsFor)
	sc.Step(`^calls from (\S+) wait (\d+) seconds for the vendor$`, func(id string, seconds int) error {
		w.appID = id
		return w.waitsFor(seconds)
	})

	sc.Step(`^the api talon-one with the host api.talon.one and the Talon.One spec$`, func() error {
		w.talonAPI("")
		return nil
	})
	sc.Step(`^the api talon-one with vendorTimeout 10s$`, func() error {
		w.talonAPI("    vendorTimeout: 10s\n")
		return nil
	})
	sc.Step(`^the integrations talon-shop-a and talon-shop-b both naming talon-one$`, func() error {
		w.addIntegration(integration("talon-shop-a", "      - talon-one\n"))
		w.addIntegration(integration("talon-shop-b", "      - talon-one\n"))
		return nil
	})
	sc.Step(`^talon-shop-a naming talon-one as it is$`, func() error {
		w.addIntegration(integration("talon-shop-a", "      - talon-one\n"))
		return nil
	})
	sc.Step(`^talon-shop-b naming talon-one with vendorTimeout 5s$`, func() error {
		w.addIntegration(integration("talon-shop-b", "      - { api: talon-one, vendorTimeout: 5s }\n"))
		return nil
	})
	sc.Step(`^either integration calls api.talon.one$`, func() error {
		for _, id := range []string{"talon-shop-a", "talon-shop-b"} {
			w.appID = id
			if err := w.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^both calls use the Talon.One spec$`, func() error {
		return w.talonTemplateFor("talon-shop-a", "talon-shop-b")
	})
	sc.Step(`^(\d+) integrations that all name the api talon-one$`, func(n int) error {
		w.talonAPI("")
		for i := 1; i <= n; i++ {
			w.addIntegration(integration(fmt.Sprintf("shop-%d", i), "      - talon-one\n"))
		}
		return nil
	})
	sc.Step(`^calls from the first and the last of them use the Talon.One spec$`, func() error {
		last := fmt.Sprintf("shop-%d", len(w.integrations))
		for _, id := range []string{"shop-1", last} {
			w.appID = id
			if err := w.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
				return err
			}
		}
		return w.talonTemplateFor("shop-1", last)
	})

	sc.Step(`^an api that lists the hosts (\S+) and (\S+)$`, func(first, second string) error {
		w.writeFile("specs/talon-one.openapi.yaml", w.exampleFile("specs/talon-one.openapi.yaml"))
		w.addIntegration(integration("talon-shop-a", w.api("talon-one", "    hosts: ["+first+", "+second+"]\n    spec: ./specs/talon-one.openapi.yaml\n")))
		return nil
	})
	sc.Step(`^calls to either host use that api$`, func() error {
		return w.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1":    "/v2/customer_sessions/{customerSessionId}",
			"PUT https://eu.api.talon.one/v2/customer_sessions/1": "/v2/customer_sessions/{customerSessionId}",
		})
	})
	sc.Step(`^an api that lists the host API.Talon.One$`, func() error {
		w.writeFile("specs/talon-one.openapi.yaml", w.exampleFile("specs/talon-one.openapi.yaml"))
		w.addIntegration(integration("talon-shop-a", w.api("talon-one", "    hosts: [API.Talon.One]\n    spec: ./specs/talon-one.openapi.yaml\n")))
		return nil
	})
	sc.Step(`^a call goes to api.talon.one$`, func() error { return w.defaultCall() })
	sc.Step(`^that api is used$`, func() error {
		return w.lastHas("url.template", "/v2/customer_sessions/{customerSessionId}")
	})

	sc.Step(`^a config file with no not-passed entry$`, func() error { return nil })
	sc.Step(`^not-passed calls use TLS origination and may go to any host$`, func() error {
		w.appID = ""
		if err := w.call("GET", "http://api.x.com/a"); err != nil {
			return err
		}
		return w.calledOver("https", "443")
	})
	sc.Step(`^a config file with a not-passed entry that sets tlsOrigination to false$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		w.addIntegration("  - id: not-passed\n    tlsOrigination: false\n")
		return nil
	})
	sc.Step(`^not-passed calls use no TLS origination$`, func() error {
		w.appID = ""
		if err := w.call("GET", "http://api.x.com/a"); err != nil {
			return err
		}
		return w.calledOver("http", "80")
	})
	sc.Step(`^two integrations that each name an api for api.talon.one, each with its own spec$`, func() error {
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		w.writeFile("specs/other.yaml", "openapi: 3.1.0\npaths:\n  /v9/other/{x}:\n    put: {}\n")
		w.addIntegration(integration("talon-shop-b", w.api("other", "    hosts: [api.talon.one]\n    spec: specs/other.yaml\n")))
		return nil
	})
	sc.Step(`^both are accepted$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		if err := w.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
			return err
		}
		w.appID = "talon-shop-b"
		if err := w.call("PUT", "https://api.talon.one/v9/other/1"); err != nil {
			return err
		}
		// A record is made just after its reply, so the two may arrive in
		// either order.
		templates := map[any]any{}
		for _, r := range w.records() {
			templates[r.attrs["hotline.integration.id"]] = r.attrs["url.template"]
		}
		if len(templates) != 2 || templates["talon-shop-a"] != "/v2/customer_sessions/{customerSessionId}" ||
			templates["talon-shop-b"] != "/v9/other/{x}" {
			return failf("got url.template by integration %v", templates)
		}
		return nil
	})
	sc.Step(`^a config file with exporters but no integrations$`, func() error {
		w.noIntegrations = true
		return nil
	})
	sc.Step(`^it is accepted and every call uses the not-passed settings$`, func() error {
		if err := w.defaultCall(); err != nil {
			return err
		}
		record, err := w.record()
		if err != nil {
			return err
		}
		if err := has(record, "hotline.integration.id", "talon-shop-a"); err != nil {
			return err
		}
		return hasNo(record, "url.full")
	})

	sc.Step(`^a config file where (.+)$`, w.brokenConfig)
	sc.Step(`^the config is refused with a message naming the problem$`, func() error {
		return w.refusedWith(w.problem)
	})
	sc.Step(`^the proxy starts and logs a warning naming (\S+)$`, func(name string) error {
		if w.startErr != nil {
			return w.startErr
		}
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, name) {
				return nil
			}
		}
		return failf("no warning names %s; the log is %s", name, w.logs.String())
	})
	sc.Step(`^a config file with three problems$`, func() error {
		w.noIntegrations = true
		w.rawConfig = w.raw("listen:\n  proxy: \"127.0.0.1:0\"\n",
			integration("a", w.apiFor("api.x.com", "    vendorTimeout: 0s\n"))+
				integration("a", w.apiFor("api.y.com", "")))
		return nil
	})
	sc.Step(`^all three problems are listed, not only the first one$`, func() error {
		return w.refusedWith("there are no exporters", "two integrations have the ID a", `vendorTimeout "0s"`)
	})
	sc.Step(`^a broken config file with an integration that has a fingerprint key$`, func() error {
		key := w.writeFile("keys/shop-a", "k3y-s3cr3t-value")
		w.addIntegration(integration("talon-shop-a", w.api("talon-one", "    hosts: [api.talon.one]\n    spec: ./specs/missing.yaml\n")) +
			"    fingerprintKey: { file: " + key + " }\n")
		return nil
	})
	sc.Step(`^the message holds no secret value$`, func() error {
		if w.startErr == nil {
			return failf("the broken config was accepted")
		}
		return w.nowhere("k3y-s3cr3t-value")
	})
	sc.Step(`^another program listens on the proxy address$`, func() error {
		l := listenLocal()
		w.listeners = append(w.listeners, l)
		w.addr = l.Addr().String()
		w.busy = w.addr
		return nil
	})
	sc.Step(`^another program listens on the admin address$`, func() error {
		l := listenLocal()
		w.listeners = append(w.listeners, l)
		w.adminAddr = l.Addr().String()
		w.busy = w.adminAddr
		return nil
	})
	sc.Step(`^the proxy does not start, and says the address is in use$`, func() error {
		return w.refusedWith(w.busy, "address already in use")
	})

	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with "([^"]+)"$`, func(method, target, header string) error {
		return w.call(method, target, header)
	})
	sc.Step(`^the proxy answers 501 Not Implemented with a Proxy-Status header$`, func() error {
		if w.resp == nil || w.resp.StatusCode != http.StatusNotImplemented || w.resp.Header.Get("Proxy-Status") == "" {
			return failf("the app got %v", w.resp)
		}
		return nil
	})
}

// templatesFor makes each call and checks the route template its record has.
func (w *world) templatesFor(calls map[string]string) error {
	for call := range calls {
		method, target, _ := strings.Cut(call, " ")
		if err := w.call(method, target); err != nil {
			return err
		}
	}
	found := map[string]bool{}
	for _, r := range w.records() {
		found[sprint(r.attrs["url.template"])] = true
	}
	for call, template := range calls {
		if !found[template] {
			return failf("no record for %s has the template %s", call, template)
		}
	}
	return nil
}

func (w *world) refusedWith(parts ...string) error {
	if w.startErr == nil {
		return failf("the config was accepted")
	}
	for _, part := range parts {
		if !strings.Contains(w.startErr.Error(), part) {
			return failf("the message %q does not name %q", w.startErr, part)
		}
	}
	return nil
}

// brokenConfig writes a config with one problem and remembers the words the
// refusal must hold.
func (w *world) brokenConfig(problem string) error {
	exporters := "exporters:\n  - otlp:\n      endpoint: " + w.receivers[0].endpoint + "\n"
	start := "listen:\n  proxy: \"127.0.0.1:0\"\n"
	one := func(extra string) string {
		return w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", extra)))
	}
	switch problem {
	case "the file is not valid YAML":
		w.rawConfig, w.problem = "listen: [proxy\n", "is not valid YAML"
	case "the file does not exist":
		w.configPath, w.problem = w.dir+"/nowhere.yaml", "nowhere.yaml"
	case "there is no proxy listen address":
		w.rawConfig = "listen:\n  admin: \"127.0.0.1:0\"\n" + exporters
		w.problem = "there is no listen.proxy address"
	case "an integration has no id":
		w.rawConfig = w.raw(start+exporters, "  - apis:\n"+w.apiFor("api.x.com", ""))
		w.problem = "an integration has no id"
	case "there are no exporters":
		w.rawConfig, w.problem = start, "there are no exporters"
	case "an exporter's temporality is neither cumulative nor delta":
		w.rawConfig = start + exporters + "      temporality: weekly\n"
		w.problem = "temporality must be cumulative or delta"
	case "an exporter's endpoint is not a valid address":
		w.rawConfig = start + exporters + "  - otlp:\n      endpoint: \"http://col%zz:4318\"\n"
		w.problem = "endpoint http://col%zz:4318"
	case "an exporter's endpoint has no http:// or https://":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: collector:4318\n"
		w.problem = "exporter collector:4318: the endpoint must start with http:// or https://"
	case "an exporter's endpoint has a query":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318/?tenant=a\n"
		w.problem = "endpoint may not have a query"
	case "an exporter's endpoint has a user name in it":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://shop:s3cr3t@collector:4318\n"
		w.problem = "endpoint may not hold a user name or password; use headers"
	case "an exporter sets tls on a plain http:// endpoint":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: http://collector:4318\n      tls:\n        ca: ca.crt\n"
		w.problem = "exporter http://collector:4318: tls needs an https:// endpoint"
	case "an exporter sets tls.cert without tls.key":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        cert: client.crt\n"
		w.problem = "exporter https://collector:4318: tls needs both cert and key, or neither"
	case "an exporter's tls.ca file does not exist":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        ca: missing-ca.crt\n"
		w.problem = "missing-ca.crt"
	case "an exporter's tls.ca file holds no certificate":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        ca: " +
			w.writeFile("otlp/empty-ca.crt", "not a certificate\n") + "\n"
		w.problem = "empty-ca.crt holds no PEM certificate"
	case "an exporter's tls.cert file does not exist":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        cert: missing.crt\n        key: missing.key\n"
		w.problem = "exporter https://collector:4318: tls: the cert or key file cannot be read"
	case "an exporter's header is neither a value, a file nor an env":
		w.rawConfig = start + exporters + "      headers:\n        Authorization: [a, b]\n"
		w.problem = "a value is written out, or read from { file: path } or { env: NAME }"
	case "an exporter's header secret file does not exist":
		w.rawConfig = start + exporters + "      headers:\n        Authorization: { file: missing-token }\n"
		w.problem = "header Authorization: the file"
	case "an exporter's header environment variable is not set":
		w.rawConfig = start + exporters + "      headers:\n        Authorization: { env: OTLP_TOKEN }\n"
		w.problem = "header Authorization: the environment variable OTLP_TOKEN is not set"
	case "an exporter sets h2c on an https:// endpoint":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      h2c: true\n"
		w.problem = "exporter https://collector:4318: h2c needs a plain http:// endpoint"
	case "an exporter's h2c is neither true nor false":
		w.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: http://collector:4318\n      h2c: maybe\n"
		w.problem = "h2c"
	case "an exporter's compression is neither gzip nor none":
		w.rawConfig = start + exporters + "      compression: zstd\n"
		w.problem = "compression must be gzip or none"
	case "an exporter's resource sets service.version":
		w.rawConfig = start + exporters + "      resource:\n        service.version: \"9\"\n"
		w.problem = "resource: service.version is the proxy's own and cannot be set"
	case `an exporter's resource has the name "my label"`:
		w.rawConfig = start + exporters + "      resource:\n        my label: x\n"
		w.problem = `resource: the name "my label" may only hold letters, digits, dots and underscores`
	case "an exporter's resource environment variable is not set":
		w.rawConfig = start + exporters + "      resource:\n        deployment.environment.name: { env: HOTLINE_ENV }\n"
		w.problem = "resource deployment.environment.name: the environment variable HOTLINE_ENV is not set"
	case "an integration's fingerprintKey is written out":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", ""))+"    fingerprintKey: s3cret\n")
		w.problem = "integration shop: fingerprintKey must be read from { file: path } or { env: NAME }, never written in the config"
	case "an integration's fingerprintKey file does not exist":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", ""))+"    fingerprintKey: { file: missing-key }\n")
		w.problem = "integration shop: fingerprintKey: the file missing-key cannot be read"
	case "an exporter's metricInterval is zero":
		w.rawConfig = start + exporters + "      metricInterval: 0s\n"
		w.problem = "metricInterval \"0s\" must be a duration above zero"
	case "an exporter's logInterval is not a duration":
		w.rawConfig = start + exporters + "      logInterval: often\n"
		w.problem = "logInterval \"often\" must be a duration above zero"
	case "an exporter's traceInterval is negative":
		w.rawConfig = start + exporters + "      traceInterval: -1s\n"
		w.problem = "traceInterval \"-1s\" must be a duration above zero"
	case "an exporter's queueSize is zero":
		w.rawConfig = start + exporters + "      queueSize: 0\n"
		w.problem = "queueSize must be at least 1"
	case "the not-passed entry has an apis list":
		w.rawConfig = w.raw(start+exporters, integration("not-passed", w.apiFor("api.x.com", "")))
	case "a field name has a typo, such as tlsOrigintion":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", "    tlsOrigintion: false\n")))
	case "there is no admin listen address":
		w.rawConfig = one("")
	case "telemetry.pprof is on but there is no admin listen address":
		w.rawConfig = w.raw(start+exporters+"telemetry:\n  pprof: true\n", integration("shop", w.apiFor("api.x.com", "")))
	case "the config still has a version field":
		w.rawConfig = "version: \"1\"\n" + one("")
	case "an exporter still sets protocol":
		w.rawConfig = w.raw(start+exporters+"      protocol: grpc\n", integration("shop", w.apiFor("api.x.com", "")))
	case "an integration changes tlsOrigination of an api it names":
		w.apiFor("api.x.com", "")
		w.rawConfig = w.raw(start+exporters, integration("shop", "      - { api: api.x.com, tlsOrigination: false }\n"))
	case `an api sets allowInternalIPs to "maybe"`:
		w.rawConfig = one(`    allowInternalIPs: maybe` + "\n")
		w.problem = "cannot unmarshal !!str `maybe` into bool"
	case "two integrations have the same ID":
		w.rawConfig = one("") + integration("shop", w.apiFor("api.y.com", ""))
		w.problem = "two integrations have the ID shop"
	case "one integration names two apis with the same host":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", ""), w.apiFor("api.x.com", "    vendorTimeout: 5s\n")))
		w.problem = "integration shop names two apis with the host api.x.com"
	case "an integration names an api that is not in apis":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", ""), "      - talon-on\n"))
		w.problem = "integration shop names the api talon-on, which is not in apis"
	case "an integration names an api with a list, not a name":
		w.rawConfig = w.raw(start+exporters, integration("shop", "      - [talon-one]\n"))
		w.problem = "an api is named by its name or by { api: name }"
	case "an api has no spec":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.api("nospec", "    hosts: [api.x.com]\n")))
		w.problem = "api nospec has no spec"
	case "an api points to a spec file that does not exist":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.api("shop", "    hosts: [api.x.com]\n    spec: ./specs/missing.yaml\n")))
		w.problem = "missing.yaml cannot be read"
	case "a spec is not an OpenAPI document":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com", "swagger: \"2.0\"\npaths: {}\n", "")))
		w.problem = "is not an OpenAPI 3.1 document"
	case "a spec is OpenAPI 3.0":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com", "openapi: 3.0.3\npaths: {}\n", "")))
		w.problem = "is not an OpenAPI 3.1 document"
	case "a schema in a spec has the type integr":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com",
			"openapi: 3.1.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, schema: { type: integr } }\n", "")))
		w.problem = "GET /a: the schema of query q cannot be used"
	case "a $ref in a spec points nowhere":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com",
			"openapi: 3.1.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { $ref: \"#/components/parameters/nope\" }\n", "")))
		w.problem = "GET /a: the $ref #/components/parameters/nope points nowhere"
	case "a $ref in a spec points to another file":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com",
			"openapi: 3.1.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, schema: { $ref: \"other.yaml#/X\" } }\n", "")))
		w.problem = "GET /a: the schema of query q cannot be used"
	case "a vendorTimeout is zero":
		w.rawConfig, w.problem = one("    vendorTimeout: 0s\n"), `vendorTimeout "0s"`
	case "a vendorTimeout is negative":
		w.rawConfig, w.problem = one("    vendorTimeout: -5s\n"), `vendorTimeout "-5s"`
	case "a vendorTimeout is not a duration":
		w.rawConfig, w.problem = one("    vendorTimeout: soon\n"), `vendorTimeout "soon"`
	case "an integration's traceFormat is zipkin":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.apiFor("api.x.com", ""))+"    traceFormat: zipkin\n")
		w.problem = "integration shop: traceFormat zipkin must be w3c, b3, xray or jaeger"
	case "an x-hotline-redact mark is not true or false":
		w.rawConfig = w.raw(start+exporters, integration("shop", w.specAPI("api.x.com",
			"openapi: 3.1.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, x-hotline-redact: maybe }\n", "")))
		w.problem = "x-hotline-redact on q must be true or false"
	default:
		return godog.ErrUndefined
	}
	return nil
}

// talonTemplateFor checks that a call from each integration got the
// Talon.One route template.
func (w *world) talonTemplateFor(ids ...string) error {
	templates := map[any]any{}
	for _, r := range w.records() {
		templates[r.attrs["hotline.integration.id"]] = r.attrs["url.template"]
	}
	for _, id := range ids {
		if templates[id] != "/v2/customer_sessions/{customerSessionId}" {
			return failf("the call from %s got url.template %v", id, templates[id])
		}
	}
	return nil
}

// waitsFor checks that a call gives up on a silent vendor after exactly
// this many seconds.
func (w *world) waitsFor(seconds int) error {
	w.settle()
	w.clock = &fakeClock{}
	wait := time.Duration(seconds) * time.Second
	early := make(chan int, 1)
	w.vendor("api.talon.one").setAnswer(func(_ http.ResponseWriter, r *http.Request) {
		early <- w.clock.pass(wait - time.Second)
		w.clock.pass(time.Second)
		<-r.Context().Done()
	})
	if err := w.defaultCall(); err != nil {
		return err
	}
	if fired := <-early; fired > 0 {
		return failf("the proxy gave up before %v", wait)
	}
	if w.resp == nil || w.resp.StatusCode != http.StatusGatewayTimeout {
		return failf("after %v the app got %v, want 504", wait, w.resp)
	}
	return nil
}
