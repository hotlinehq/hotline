package proxy_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func configSteps(sc *godog.ScenarioContext, sut *configFileSUT) {
	sc.Step(`^a valid config file$`, func() error {
		sut.otlp.receivers = append(sut.otlp.receivers, newReceiver())
		return nil
	})
	sc.Step(`^the proxy (?:loads|reports) (?:it|the config|the problem)$`, func() error {
		sut.proxy.startErr = nil
		_ = sut.proxy.start()
		return nil
	})
	sc.Step(`^it is accepted$`, func() error {
		return sut.proxy.startErr
	})
	sc.Step(`^a valid config file named (\S+)$`, func(name string) error {
		sut.proxy.configName = name
		return nil
	})
	sc.Step(`^every record carries hotline.config.version (\S+) and a fingerprint$`, func(version string) error {
		if err := sut.app.defaultCall(); err != nil {
			return err
		}
		record, err := sut.otlp.lastRecord()
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
		if sut.proxy.startErr != nil {
			return sut.proxy.startErr
		}
		if err := sut.app.defaultCall(); err != nil {
			return err
		}
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusOK {
			return failf("a call to %s got %v", sut.proxy.addr, sut.app.resp)
		}
		return nil
	})
	sc.Step(`^every OTLP exporter is read with its endpoint$`, func() error {
		sut.proxy.settle()
		for _, r := range sut.otlp.receivers {
			if records := r.got(); len(records) != 1 {
				return failf("the exporter at %s got %d records", r.endpoint, len(records))
			}
		}
		return nil
	})
	sc.Step(`^every api is read with its hosts and the spec it points to$`, func() error {
		return sut.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1":   "/v2/customer_sessions/{customerSessionId}",
			"GET https://api.stripe.com/v1/payment_intents/pi_1": "/v1/payment_intents/{intent}",
		})
	})
	sc.Step(`^every integration is read with the apis it names$`, func() error {
		for _, r := range sut.otlp.records() {
			if err := has(r.attrs, "hotline.integration.id", "talon-shop-a"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^spec files are found relative to the folder of the config file$`, func() error {
		if !strings.HasPrefix(sut.proxy.configPath, sut.proxy.dir) {
			return failf("the config file %s is not in the test folder", sut.proxy.configPath)
		}
		return nil
	})
	sc.Step(`^it loads together with both of its specs$`, func() error {
		if sut.proxy.startErr != nil {
			return sut.proxy.startErr
		}
		return sut.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1": "/v2/customer_sessions/{customerSessionId}",
			"GET https://api.stripe.com/v1/payment_intents":    "/v1/payment_intents",
		})
	})

	sc.Step(`^an api that (does not set tlsOrigination|sets tlsOrigination to false|does not set vendorTimeout|sets vendorTimeout to 10s)$`, func(sets string) error {
		extra := map[string]string{
			"sets tlsOrigination to false": "    tlsOrigination: false\n",
			"sets vendorTimeout to 10s":    "    vendorTimeout: 10s\n",
		}[sets]
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI(extra)))
		return nil
	})
	sc.Step(`^TLS origination is (on|off)$`, func(on string) error {
		if err := sut.app.call("GET", "http://api.talon.one/v2/x"); err != nil {
			return err
		}
		if on == "on" {
			return sut.app.calledOver("https", "443")
		}
		return sut.app.calledOver("http", "80")
	})
	sc.Step(`^the proxy waits (\d+) seconds for the vendor$`, sut.waitsFor)
	sc.Step(`^calls from (\S+) wait (\d+) seconds for the vendor$`, func(id string, seconds int) error {
		sut.app.appID = id
		return sut.waitsFor(seconds)
	})

	sc.Step(`^the api talon-one with the host api.talon.one and the Talon.One spec$`, func() error {
		sut.apis.talonAPI("")
		return nil
	})
	sc.Step(`^the api talon-one with vendorTimeout 10s$`, func() error {
		sut.apis.talonAPI("    vendorTimeout: 10s\n")
		return nil
	})
	sc.Step(`^the integrations talon-shop-a and talon-shop-b both naming talon-one$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", "      - talon-one\n"))
		sut.apis.addIntegration(integration("talon-shop-b", "      - talon-one\n"))
		return nil
	})
	sc.Step(`^talon-shop-a naming talon-one as it is$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", "      - talon-one\n"))
		return nil
	})
	sc.Step(`^talon-shop-b naming talon-one with vendorTimeout 5s$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-b", "      - { api: talon-one, vendorTimeout: 5s }\n"))
		return nil
	})
	sc.Step(`^either integration calls api.talon.one$`, func() error {
		for _, id := range []string{"talon-shop-a", "talon-shop-b"} {
			sut.app.appID = id
			if err := sut.app.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^both calls use the Talon.One spec$`, func() error {
		return sut.talonTemplateFor("talon-shop-a", "talon-shop-b")
	})
	sc.Step(`^(\d+) integrations that all name the api talon-one$`, func(n int) error {
		sut.apis.talonAPI("")
		for i := 1; i <= n; i++ {
			sut.apis.addIntegration(integration(fmt.Sprintf("shop-%d", i), "      - talon-one\n"))
		}
		return nil
	})
	sc.Step(`^calls from the first and the last of them use the Talon.One spec$`, func() error {
		last := fmt.Sprintf("shop-%d", len(sut.apis.integrations))
		for _, id := range []string{"shop-1", last} {
			sut.app.appID = id
			if err := sut.app.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
				return err
			}
		}
		return sut.talonTemplateFor("shop-1", last)
	})

	sc.Step(`^an api that lists the hosts (\S+) and (\S+)$`, func(first, second string) error {
		sut.proxy.writeFile("specs/talon-one.openapi.yaml", sut.proxy.exampleFile("specs/talon-one.openapi.yaml"))
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.api("talon-one", "    hosts: ["+first+", "+second+"]\n    spec: ./specs/talon-one.openapi.yaml\n")))
		return nil
	})
	sc.Step(`^calls to either host use that api$`, func() error {
		return sut.templatesFor(map[string]string{
			"PUT https://api.talon.one/v2/customer_sessions/1":    "/v2/customer_sessions/{customerSessionId}",
			"PUT https://eu.api.talon.one/v2/customer_sessions/1": "/v2/customer_sessions/{customerSessionId}",
		})
	})
	sc.Step(`^an api that lists the host API.Talon.One$`, func() error {
		sut.proxy.writeFile("specs/talon-one.openapi.yaml", sut.proxy.exampleFile("specs/talon-one.openapi.yaml"))
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.api("talon-one", "    hosts: [API.Talon.One]\n    spec: ./specs/talon-one.openapi.yaml\n")))
		return nil
	})
	sc.Step(`^a call goes to api.talon.one$`, func() error { return sut.app.defaultCall() })
	sc.Step(`^that api is used$`, func() error {
		return sut.otlp.lastHas("url.template", "/v2/customer_sessions/{customerSessionId}")
	})

	sc.Step(`^a config file with no not-passed entry$`, func() error { return nil })
	sc.Step(`^not-passed calls use TLS origination and may go to any host$`, func() error {
		sut.app.appID = ""
		if err := sut.app.call("GET", "http://api.x.com/a"); err != nil {
			return err
		}
		return sut.app.calledOver("https", "443")
	})
	sc.Step(`^a config file with a not-passed entry that sets tlsOrigination to false$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("")))
		sut.apis.addIntegration("  - id: not-passed\n    tlsOrigination: false\n")
		return nil
	})
	sc.Step(`^not-passed calls use no TLS origination$`, func() error {
		sut.app.appID = ""
		if err := sut.app.call("GET", "http://api.x.com/a"); err != nil {
			return err
		}
		return sut.app.calledOver("http", "80")
	})
	sc.Step(`^two integrations that each name an api for api.talon.one, each with its own spec$`, func() error {
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("")))
		sut.proxy.writeFile("specs/other.yaml", "openapi: 3.2.0\npaths:\n  /v9/other/{x}:\n    put: {}\n")
		sut.apis.addIntegration(integration("talon-shop-b", sut.apis.api("other", "    hosts: [api.talon.one]\n    spec: specs/other.yaml\n")))
		return nil
	})
	sc.Step(`^both are accepted$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		if err := sut.app.call("PUT", "https://api.talon.one/v2/customer_sessions/1"); err != nil {
			return err
		}
		sut.app.appID = "talon-shop-b"
		if err := sut.app.call("PUT", "https://api.talon.one/v9/other/1"); err != nil {
			return err
		}
		// A record is made just after its response, so the two may arrive in
		// either order.
		templates := map[any]any{}
		for _, r := range sut.otlp.records() {
			templates[r.attrs["hotline.integration.id"]] = r.attrs["url.template"]
		}
		if len(templates) != 2 || templates["talon-shop-a"] != "/v2/customer_sessions/{customerSessionId}" ||
			templates["talon-shop-b"] != "/v9/other/{x}" {
			return failf("got url.template by integration %v", templates)
		}
		return nil
	})
	sc.Step(`^a config file with exporters but no integrations$`, func() error {
		sut.apis.noIntegrations = true
		return nil
	})
	sc.Step(`^it is accepted and every call uses the not-passed settings$`, func() error {
		if err := sut.app.defaultCall(); err != nil {
			return err
		}
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		if err := has(record, "hotline.integration.id", "not-passed"); err != nil {
			return err
		}
		return hasNo(record, "url.full")
	})

	sc.Step(`^a config file where (.+)$`, sut.brokenConfig)
	sc.Step(`^the config is refused with a message naming the problem$`, func() error {
		return sut.proxy.refusedWith(sut.proxy.problem)
	})
	sc.Step(`^the proxy starts and logs a warning naming (.+)$`, func(name string) error {
		if sut.proxy.startErr != nil {
			return sut.proxy.startErr
		}
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, name) {
				return nil
			}
		}
		return failf("no warning names %s; the log is %s", name, sut.proxy.logs.String())
	})
	sc.Step(`^a config file with three problems$`, func() error {
		sut.apis.noIntegrations = true
		sut.proxy.rawConfig = sut.apis.raw("listeners:\n  apps:\n    address: \"tcp://127.0.0.1:0\"\n",
			integration("a", sut.apis.apiFor("api.x.com", "    vendorTimeout: 0s\n"))+
				integration("a", sut.apis.apiFor("api.y.com", "")))
		return nil
	})
	sc.Step(`^all three problems are listed, not only the first one$`, func() error {
		return sut.proxy.refusedWith("there are no exporters", "two integrations have the ID a", `vendorTimeout "0s"`)
	})
	sc.Step(`^a broken config file with an exporter token read from a file$`, func() error {
		key := sut.proxy.writeFile("keys/otlp-token", "k3y-s3cr3t-value")
		sut.otlp.exporterExtra = []string{"      headers:\n        Authorization: { file: " + key + " }\n"}
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.api("talon-one", "    hosts: [api.talon.one]\n    spec: ./specs/missing.yaml\n")))
		return nil
	})
	sc.Step(`^the message holds no secret value$`, func() error {
		if sut.proxy.startErr == nil {
			return failf("the broken config was accepted")
		}
		return sut.otlp.nowhere("k3y-s3cr3t-value")
	})
	sc.Step(`^another program listens on the proxy address$`, func() error {
		l := listenLocal()
		sut.proxy.opened = append(sut.proxy.opened, l)
		sut.proxy.addr = l.Addr().String()
		sut.proxy.busy = sut.proxy.addr
		return nil
	})
	sc.Step(`^another program listens on the admin address$`, func() error {
		l := listenLocal()
		sut.proxy.opened = append(sut.proxy.opened, l)
		sut.listeners.adminAddr = l.Addr().String()
		sut.proxy.busy = sut.listeners.adminAddr
		return nil
	})
	sc.Step(`^the proxy does not start, and says the address is in use$`, func() error {
		return sut.proxy.refusedWith(sut.proxy.busy, "address already in use")
	})

	sc.Step(`^an app sends "([A-Z]+) ([^"]+)" with "([^"]+)"$`, func(method, target, header string) error {
		return sut.app.call(method, target, header)
	})
	sc.Step(`^the proxy answers 501 Not Implemented with a Proxy-Status header$`, func() error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusNotImplemented || sut.app.resp.Header.Get("Proxy-Status") == "" {
			return failf("the app got %v", sut.app.resp)
		}
		return nil
	})
}

// templatesFor makes each call and checks the route template its record has.
func (sut *configFileSUT) templatesFor(calls map[string]string) error {
	for call := range calls {
		method, target, _ := strings.Cut(call, " ")
		if err := sut.app.call(method, target); err != nil {
			return err
		}
	}
	found := map[string]bool{}
	for _, r := range sut.otlp.records() {
		found[sprint(r.attrs["url.template"])] = true
	}
	for call, template := range calls {
		if !found[template] {
			return failf("no record for %s has the template %s", call, template)
		}
	}
	return nil
}

func (sut *proxySUT) refusedWith(parts ...string) error {
	if sut.startErr == nil {
		return failf("the config was accepted")
	}
	for _, part := range parts {
		if !strings.Contains(sut.startErr.Error(), part) {
			return failf("the message %q does not name %q", sut.startErr, part)
		}
	}
	return nil
}

// brokenConfig writes a config with one problem and remembers the words the
// refusal must hold.
func (sut *configFileSUT) brokenConfig(problem string) error {
	exporters := "exporters:\n  - otlp:\n      endpoint: " + sut.otlp.receivers[0].endpoint + "\n"
	start := "listeners:\n  apps:\n    address: \"tcp://127.0.0.1:0\"\n"
	ownTelemetry := "telemetry:\n  otlp:\n    endpoint: " + sut.otlp.receivers[0].endpoint + "\n"
	one := func(extra string) string {
		return sut.apis.raw(start+exporters, integration("shop", sut.apis.apiFor("api.x.com", extra)))
	}
	switch problem {
	case "the file is not valid YAML":
		sut.proxy.rawConfig, sut.proxy.problem = "listen: [proxy\n", "is not valid YAML"
	case "the file does not exist":
		sut.proxy.configPath, sut.proxy.problem = sut.proxy.dir+"/nowhere.yaml", "nowhere.yaml"
	case "there are no listeners":
		sut.proxy.rawConfig = "admin:\n  address: \"tcp://127.0.0.1:0\"\n" + exporters
		sut.proxy.problem = "there are no listeners"
	case "an integration has no id":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, "  - apis:\n"+sut.apis.apiFor("api.x.com", ""))
		sut.proxy.problem = "an integration has no id"
	case "there are no exporters":
		sut.proxy.rawConfig, sut.proxy.problem = start, "there are no exporters"
	case "the telemetry section's temporality is neither cumulative nor delta":
		sut.proxy.rawConfig = start + exporters + ownTelemetry + "    temporality: weekly\n"
		sut.proxy.problem = "temporality must be cumulative or delta"
	case "an exporter's endpoint is not a valid address":
		sut.proxy.rawConfig = start + exporters + "  - otlp:\n      endpoint: \"http://col%zz:4318\"\n"
		sut.proxy.problem = "endpoint http://col%zz:4318"
	case "an exporter's endpoint has no http:// or https://":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: collector:4318\n"
		sut.proxy.problem = "exporter collector:4318: the endpoint must start with http:// or https://"
	case "an exporter's endpoint has a query":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318/?tenant=a\n"
		sut.proxy.problem = "endpoint may not have a query"
	case "an exporter's endpoint has a user name in it":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://shop:s3cr3t@collector:4318\n"
		sut.proxy.problem = "endpoint may not hold a user name or password; use headers"
	case "an exporter sets tls on a plain http:// endpoint":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: http://collector:4318\n      tls:\n        ca: ca.crt\n"
		sut.proxy.problem = "exporter http://collector:4318: tls needs an https:// endpoint"
	case "an exporter sets tls.cert without tls.key":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        cert: client.crt\n"
		sut.proxy.problem = "exporter https://collector:4318: tls needs both cert and key, or neither"
	case "an exporter's tls.ca file does not exist":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        ca: missing-ca.crt\n"
		sut.proxy.problem = "missing-ca.crt"
	case "an exporter's tls.ca file holds no certificate":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        ca: " +
			sut.proxy.writeFile("otlp/empty-ca.crt", "not a certificate\n") + "\n"
		sut.proxy.problem = "empty-ca.crt holds no PEM certificate"
	case "an exporter's tls.cert file does not exist":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      tls:\n        cert: missing.crt\n        key: missing.key\n"
		sut.proxy.problem = "exporter https://collector:4318: tls: the cert or key file cannot be read"
	case "an exporter's header is neither a value, a file nor an env":
		sut.proxy.rawConfig = start + exporters + "      headers:\n        Authorization: [a, b]\n"
		sut.proxy.problem = "a value is written out, or read from { file: path } or { env: NAME }"
	case "an exporter's header secret file does not exist":
		sut.proxy.rawConfig = start + exporters + "      headers:\n        Authorization: { file: missing-token }\n"
		sut.proxy.problem = "header Authorization: the file"
	case "an exporter's header environment variable is not set":
		sut.proxy.rawConfig = start + exporters + "      headers:\n        Authorization: { env: OTLP_TOKEN }\n"
		sut.proxy.problem = "header Authorization: the environment variable OTLP_TOKEN is not set"
	case "an exporter sets h2c on an https:// endpoint":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: https://collector:4318\n      h2c: true\n"
		sut.proxy.problem = "exporter https://collector:4318: h2c needs a plain http:// endpoint"
	case "an exporter's h2c is neither true nor false":
		sut.proxy.rawConfig = start + "exporters:\n  - otlp:\n      endpoint: http://collector:4318\n      h2c: maybe\n"
		sut.proxy.problem = "h2c"
	case "an exporter's compression is neither gzip nor none":
		sut.proxy.rawConfig = start + exporters + "      compression: zstd\n"
		sut.proxy.problem = "compression must be gzip or none"
	case "an exporter sets a resource":
		sut.proxy.rawConfig = start + exporters + "      resource:\n        deployment.environment.name: staging\n"
		sut.proxy.problem = "resource is no longer set per exporter: use records.transform labels"
	case "a records labels step sets service.version":
		sut.proxy.rawConfig = start + exporters + "records:\n  transform:\n    - labels:\n        service.version: \"9\"\n"
		sut.proxy.problem = "records.transform labels: service.version is the proxy's own and cannot be set"
	case `a records labels step has the name "my label"`:
		sut.proxy.rawConfig = start + exporters + "records:\n  transform:\n    - labels:\n        my label: x\n"
		sut.proxy.problem = `records.transform labels: the name "my label" may only hold letters, digits, dots and underscores`
	case "a records labels environment variable is not set":
		sut.proxy.rawConfig = start + exporters + "records:\n  transform:\n    - labels:\n        deployment.environment.name: { env: HOTLINE_ENV }\n"
		sut.proxy.problem = "records.transform labels deployment.environment.name: the environment variable HOTLINE_ENV is not set"
	case "a records transform step is not labels":
		sut.proxy.rawConfig = start + exporters + "records:\n  transform:\n    - rename: { a: b }\n"
		sut.proxy.problem = "records.transform: each step is labels"
	case "a records labels step is not a map":
		sut.proxy.rawConfig = start + exporters + "records:\n  transform:\n    - labels: [a]\n"
		sut.proxy.problem = "records.transform labels: "
	case "the telemetry section's resource sets service.version":
		sut.proxy.rawConfig = start + exporters + ownTelemetry + "    resource:\n      service.version: \"9\"\n"
		sut.proxy.problem = "resource: service.version is the proxy's own and cannot be set"
	case "the telemetry section sets queueFolder":
		sut.proxy.rawConfig = start + exporters + ownTelemetry + "    queueFolder: queue\n    queueDisk: 1GiB\n"
		sut.proxy.problem = "queueFolder and queueDisk are for record exporters only"
	case "the telemetry section's metricInterval is zero":
		sut.proxy.rawConfig = start + exporters + ownTelemetry + "    metricInterval: 0s\n"
		sut.proxy.problem = "metricInterval \"0s\" must be a duration above zero"
	case "an exporter's logInterval is not a duration":
		sut.proxy.rawConfig = start + exporters + "      logInterval: often\n"
		sut.proxy.problem = "logInterval \"often\" must be a duration above zero"
	case "an exporter's traceInterval is negative":
		sut.proxy.rawConfig = start + exporters + "      traceInterval: -1s\n"
		sut.proxy.problem = "traceInterval \"-1s\" must be a duration above zero"
	case "the ledger's maxRecords is zero":
		sut.proxy.rawConfig = start + exporters + "ledger:\n  batch:\n    maxRecords: 0\n"
		sut.proxy.problem = "ledger.batch: maxRecords must be at least 1"
	case "an exporter sets queueFolder without queueDisk":
		sut.proxy.rawConfig = start + exporters + "      queueFolder: queue\n"
		sut.proxy.problem = "queueFolder needs queueDisk"
	case "an exporter sets queueDisk without queueFolder":
		sut.proxy.rawConfig = start + exporters + "      queueDisk: 1GiB\n"
		sut.proxy.problem = "queueDisk needs queueFolder"
	case "an exporter's queueDisk is under 64MiB":
		sut.proxy.rawConfig = start + exporters + "      queueFolder: queue\n      queueDisk: 32MiB\n"
		sut.proxy.problem = "queueDisk must be at least 64MiB"
	case "an exporter's queueFolder cannot be written to":
		sut.proxy.rawConfig = start + exporters + "      queueFolder: /proc/hotline-queue\n      queueDisk: 1GiB\n"
		sut.proxy.problem = "the queueFolder cannot be written to"
	case "two exporters share one queueFolder":
		sut.proxy.rawConfig = start + exporters + "      queueFolder: queue\n      queueDisk: 1GiB\n" +
			"  - otlp:\n      endpoint: http://localhost:4319\n      queueFolder: queue\n      queueDisk: 1GiB\n"
		sut.proxy.problem = "two exporters keep their queue in the folder queue"
	case "an exporter's queueSize is zero":
		sut.proxy.rawConfig = start + exporters + "      queueSize: 0\n"
		sut.proxy.problem = "queueSize must be at least 1"
	case "the not-passed entry has an apis list":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("not-passed", sut.apis.apiFor("api.x.com", "")))
	case "a field name has a typo, such as tlsOrigintion":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.apiFor("api.x.com", "    tlsOrigintion: false\n")))
	case "there is no admin address":
		sut.proxy.rawConfig = one("")
	case "telemetry.pprof is on but there is no admin address":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters+"telemetry:\n  pprof: true\n", integration("shop", sut.apis.apiFor("api.x.com", "")))
	case "the config still has a listen section":
		sut.proxy.rawConfig = "listen:\n  proxy: \"127.0.0.1:0\"\n" + one("")
	case "the config still has an issuers section":
		sut.proxy.rawConfig = "issuers:\n  shop: { cert: a.pem, key: b.pem }\n" + one("")
	case "the config still has a version field":
		sut.proxy.rawConfig = "version: \"1\"\n" + one("")
	case "an exporter still sets protocol":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters+"      protocol: grpc\n", integration("shop", sut.apis.apiFor("api.x.com", "")))
	case "an integration changes tlsOrigination of an api it names":
		sut.apis.apiFor("api.x.com", "")
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", "      - { api: api.x.com, tlsOrigination: false }\n"))
	case `an api sets allowInternalIPs to "maybe"`:
		sut.proxy.rawConfig = one(`    allowInternalIPs: maybe` + "\n")
		sut.proxy.problem = "cannot unmarshal !!str `maybe` into bool"
	case "two integrations have the same ID":
		sut.proxy.rawConfig = one("") + integration("shop", sut.apis.apiFor("api.y.com", ""))
		sut.proxy.problem = "two integrations have the ID shop"
	case "one integration names two apis with the same host":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.apiFor("api.x.com", ""), sut.apis.apiFor("api.x.com", "    vendorTimeout: 5s\n")))
		sut.proxy.problem = "integration shop names two apis with the host api.x.com"
	case "an integration names an api that is not in apis":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.apiFor("api.x.com", ""), "      - talon-on\n"))
		sut.proxy.problem = "integration shop names the api talon-on, which is not in apis"
	case "an integration names an api with a list, not a name":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", "      - [talon-one]\n"))
		sut.proxy.problem = "an api is named by its name or by { api: name }"
	case "an api has no spec":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.api("nospec", "    hosts: [api.x.com]\n")))
		sut.proxy.problem = "api nospec has no spec"
	case "an api points to a spec file that does not exist":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.api("shop", "    hosts: [api.x.com]\n    spec: ./specs/missing.yaml\n")))
		sut.proxy.problem = "missing.yaml cannot be read"
	case "a spec is not YAML":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com", "openapi: [3.2.0\n", "")))
		sut.proxy.problem = "is not YAML or JSON"
	case "a spec is Swagger 2.0, with no openapi line":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com", "swagger: \"2.0\"\npaths: {}\n", "")))
		sut.proxy.problem = "has no openapi line, but Hotline reads OpenAPI 3.2 only: change the line to openapi: 3.2.0"
	case "a spec is OpenAPI 3.0", "a spec is OpenAPI 3.1", "a spec is OpenAPI 3.20":
		version := strings.TrimPrefix(problem, "a spec is OpenAPI ") + ".0"
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com", "openapi: "+version+"\npaths: {}\n", "")))
		sut.proxy.problem = "says openapi " + version + ", but Hotline reads OpenAPI 3.2 only: change the line to openapi: 3.2.0"
	case "a schema in a spec has the type integr":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com",
			"openapi: 3.2.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, schema: { type: integr } }\n", "")))
		sut.proxy.problem = "GET /a: the schema of query q cannot be used"
	case "a $ref in a spec points nowhere":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com",
			"openapi: 3.2.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { $ref: \"#/components/parameters/nope\" }\n", "")))
		sut.proxy.problem = "GET /a: the $ref #/components/parameters/nope points nowhere"
	case "a $ref in a spec points to another file":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com",
			"openapi: 3.2.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, schema: { $ref: \"other.yaml#/X\" } }\n", "")))
		sut.proxy.problem = "GET /a: the schema of query q cannot be used"
	case "a vendorTimeout is zero":
		sut.proxy.rawConfig, sut.proxy.problem = one("    vendorTimeout: 0s\n"), `vendorTimeout "0s"`
	case "a vendorTimeout is negative":
		sut.proxy.rawConfig, sut.proxy.problem = one("    vendorTimeout: -5s\n"), `vendorTimeout "-5s"`
	case "a vendorTimeout is not a duration":
		sut.proxy.rawConfig, sut.proxy.problem = one("    vendorTimeout: soon\n"), `vendorTimeout "soon"`
	case "an integration's traceFormat is zipkin":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.apiFor("api.x.com", ""))+"    traceFormat: zipkin\n")
		sut.proxy.problem = "integration shop: traceFormat zipkin must be w3c, b3, xray or jaeger"
	case "an x-hotline-redact mark is not true or false":
		sut.proxy.rawConfig = sut.apis.raw(start+exporters, integration("shop", sut.apis.specAPI("api.x.com",
			"openapi: 3.2.0\npaths:\n  /a:\n    get:\n      parameters:\n        - { name: q, in: query, x-hotline-redact: maybe }\n", "")))
		sut.proxy.problem = "x-hotline-redact on q must be true or false"
	default:
		return godog.ErrUndefined
	}
	return nil
}

// talonTemplateFor checks that a call from each integration got the
// Talon.One route template.
func (sut *configFileSUT) talonTemplateFor(ids ...string) error {
	templates := map[any]any{}
	for _, r := range sut.otlp.records() {
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
func (sut *configFileSUT) waitsFor(seconds int) error {
	sut.proxy.settle()
	sut.proxy.clock = &fakeClock{}
	wait := time.Duration(seconds) * time.Second
	// The ledger closes batches on the same clock: keep its timers out of
	// the count of timers that fired.
	if !strings.Contains(sut.proxy.topExtra, "ledger:") {
		sut.proxy.topExtra += "ledger:\n  batch:\n    every: 1h\n"
		if sut.proxy.wroteConfig {
			sut.proxy.replaceFile("hotline-proxy.yaml", sut.proxy.configText())
		}
	}
	early := make(chan int, 1)
	sut.vendors.vendor("api.talon.one").setAnswer(func(_ http.ResponseWriter, r *http.Request) {
		early <- sut.proxy.clock.pass(wait - time.Second)
		sut.proxy.clock.pass(time.Second)
		<-r.Context().Done()
	})
	if err := sut.app.defaultCall(); err != nil {
		return err
	}
	if fired := <-early; fired > 0 {
		return failf("the proxy gave up before %v", wait)
	}
	if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusGatewayTimeout {
		return failf("after %v the app got %v, want 504", wait, sut.app.resp)
	}
	return nil
}

// configFileSUT is the config file itself: what the proxy reads from it,
// what it refuses, and how it reloads a changed one.
type configFileSUT struct {
	proxy       *proxySUT
	vendors     *vendorsSUT
	certs       *certsSUT
	app         *appSUT
	apis        *apisSUT
	otlp        *otlpSUT
	tlsIngress  *tlsIngressSUT
	connect     *connectSUT
	telemetry   *telemetrySUT
	capture     *captureSUT
	transparent *transparentSUT
	listeners   *listenersSUT
}

func newConfigFileSUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, apis *apisSUT, otlp *otlpSUT, tlsIngress *tlsIngressSUT, connect *connectSUT, telemetry *telemetrySUT, capture *captureSUT, transparent *transparentSUT, listeners *listenersSUT) *configFileSUT {
	return &configFileSUT{proxy: root, vendors: vendors, certs: certs, app: app, apis: apis, otlp: otlp, tlsIngress: tlsIngress, connect: connect, telemetry: telemetry, capture: capture, transparent: transparent, listeners: listeners}
}

// AddSteps adds the steps of the config file.
func (sut *configFileSUT) AddSteps(sc *godog.ScenarioContext) {
	configSteps(sc, sut)
	reloadSteps(sc, sut)
}
