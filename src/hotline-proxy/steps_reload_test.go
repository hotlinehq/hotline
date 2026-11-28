package proxy_test

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// noPathsSpec is a valid spec with no routes, so calls get no url.template.
const noPathsSpec = `openapi: 3.2.0
info: { title: Talon.One, version: "1" }
paths: {}
`

// noPathsOverlay removes every route of a spec.
const noPathsOverlay = `overlay: 1.1.0
info: { title: No paths, version: "1" }
actions:
  - target: $.paths.*
    remove: true
`

func reloadSteps(sc *godog.ScenarioContext, sut *reloadSUT) {
	sc.Step(`^a config with integration talon-shop-a for Talon\.One$`, func() error {
		sut.vendors.vendor("api.talon.one")
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("")))
		return nil
	})
	sc.Step(`^an app calls Talon\.One as (\S+)$`, func(id string) error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		sut.app.appID = id
		return sut.app.defaultCall()
	})
	sc.Step(`^the config file changes so that ([^,]+)$`, func(change string) error {
		return sut.changeConfig(change)
	})
	sc.Step(`^the config file changes so that ([^,]+), and talon-shop-b also uses Talon\.One$`, func(change string) error {
		sut.apis.addIntegration(integration("talon-shop-b", sut.apis.talonAPI("")))
		return sut.changeConfig(change)
	})
	sc.Step(`^the Talon\.One spec file changes so that it has no paths$`, func() error {
		sut.proxy.writeFile("specs/talon-one.openapi.yaml", noPathsSpec)
		return nil
	})
	sc.Step(`^the overlay file no-paths\.yaml arrives, removing every path$`, func() error {
		sut.proxy.writeFile("overlays/no-paths.yaml", noPathsOverlay)
		return nil
	})
	sc.Step(`^the proxy checks its files$`, func() error {
		if err := sut.proxy.start(); err != nil {
			return err
		}
		sut.proxy.running.CheckFiles()
		return nil
	})
	sc.Step(`^the proxy is told to reload$`, func() error {
		sut.proxy.running.Reload()
		return nil
	})

	sc.Step(`^record (\d+) has no url\.template$`, func(n int) error {
		record, err := sut.nth(n)
		if err != nil {
			return err
		}
		return hasNo(record, "url.template")
	})
	sc.Step(`^record (\d+) has url\.template (\S+)$`, func(n int, template string) error {
		record, err := sut.nth(n)
		if err != nil {
			return err
		}
		return has(record, "url.template", template)
	})
	sc.Step(`^record (\d+) and record (\d+) carry (the same|different) (\S+)$`, func(a, b int, same, key string) error {
		first, err := sut.nth(a)
		if err != nil {
			return err
		}
		second, err := sut.nth(b)
		if err != nil {
			return err
		}
		if first[key] == nil || second[key] == nil {
			return failf("%s is missing: %v and %v", key, first[key], second[key])
		}
		if (first[key] == second[key]) != (same == "the same") {
			return failf("%s is %v and %v, want %s", key, first[key], second[key], same)
		}
		return nil
	})
	sc.Step(`^the proxy (logs|does not log) that the config was reloaded$`, func(logs string) error {
		if strings.Contains(sut.proxy.logs.String(), `msg="config reloaded"`) != (logs == "logs") {
			return failf("the proxy %s that the config was reloaded; the log is %s", logs, sut.proxy.logs.String())
		}
		return nil
	})
	sc.Step(`^the proxy does not log that a new TLS certificate is in use$`, func() error {
		if strings.Contains(sut.proxy.logs.String(), "new TLS certificate in use") {
			return failf("the proxy logged a new TLS certificate; the log is %s", sut.proxy.logs.String())
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that the reload failed, naming (.+)$`, func(name string) error {
		return sut.proxy.warned("config reload failed", name)
	})
	sc.Step(`^the proxy logs a warning that (.+) needs a restart$`, func(setting string) error {
		return sut.proxy.warned(setting, "needs a restart")
	})
}

// changeConfig rewrites the config file with one change.
func (sut *reloadSUT) changeConfig(change string) error {
	if err := sut.proxy.start(); err != nil {
		return err
	}
	switch change {
	case "talon-shop-b also uses Talon.One":
		sut.apis.addIntegration(integration("talon-shop-b", sut.apis.talonAPI("")))
	case "it is not valid YAML":
		sut.proxy.writeFile("hotline-proxy.yaml", "listen: [proxy\n")
		return nil
	case "talon-shop-a's spec file is missing":
		sut.apis.renameFirstSpec("talon-one.openapi.yaml", "missing.yaml")
	case "talon-shop-a's spec says OpenAPI 3.1":
		sut.apis.renameFirstSpec("talon-one.openapi.yaml", "talon-one-3.1.openapi.yaml")
		sut.proxy.writeFile("specs/talon-one-3.1.openapi.yaml", "openapi: 3.1.0\npaths: {}\n")
	case "Talon.One has the overlay no-paths.yaml":
		sut.apis.addToFirstAPI("    overlays: [./overlays/no-paths.yaml]\n")
	case "the proxy listener moves to another port":
		addr := sut.proxy.addr
		sut.proxy.addr = sut.proxy.ports.take()
		defer func() { sut.proxy.addr = addr }()
	case "the admin section is removed":
		sut.listeners.leaveOutAdmin()
	case "the admin port moves to another port":
		sut.listeners.useAdminAddress(sut.proxy.ports.take())
	case "there is one more exporter":
		sut.otlp.addExporterThatIsDown()
	case "the records labels change":
		sut.otlp.addLabels("deployment.environment.name: staging")
	case "the tls part is added":
		cert := sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		sut.tlsIngress.writeKeyPair(cert, cert)
		sut.tlsIngress.useAppCertificate()
	case "the telemetry section turns off go.*":
		sut.telemetry.turnOffMetrics("go.*")
	case "telemetry.pprof is turned on":
		sut.telemetry.turnOnProfilePages()
	case "the transparent listener is added":
		sut.certs.useAuthority(newAuthority())
		sut.transparent.listenOn(sut.proxy.ports.take(), "customer")
	case "the connect part is added":
		sut.connect.useInterceptionAuthority(newAuthority())
	case "the capture listener is added":
		sut.connect.useInterceptionAuthority(newAuthority())
		sut.capture.useSocketFile(filepath.Join(sut.proxy.dir, "capture.sock"))
	default:
		return godog.ErrPending
	}
	sut.proxy.writeFile("hotline-proxy.yaml", sut.proxy.configText())
	return nil
}

// nth is the scenario's record number n, counted from 1, once the proxy
// has sent everything.
func (sut *reloadSUT) nth(n int) (map[string]any, error) {
	records := sut.otlp.records()
	if n > len(records) {
		return nil, failf("there are %d records, not %d", len(records), n)
	}
	return records[n-1].attrs, nil
}

// warned checks that one warning in the log holds every part.
func (sut *proxySUT) warned(parts ...string) error {
	for _, line := range strings.Split(sut.logs.String(), "\n") {
		if !strings.Contains(line, "level=WARN") {
			continue
		}
		all := true
		for _, part := range parts {
			all = all && strings.Contains(line, part)
		}
		if all {
			return nil
		}
	}
	return failf("no warning says %q; the log is %s", parts, sut.logs.String())
}

// reloadSUT is a running proxy whose files change: what it reloads, what
// needs a restart, and what it keeps when a change is wrong.
type reloadSUT struct {
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

func newReloadSUT(root *proxySUT, vendors *vendorsSUT, certs *certsSUT, app *appSUT, apis *apisSUT, otlp *otlpSUT, tlsIngress *tlsIngressSUT, connect *connectSUT, telemetry *telemetrySUT, capture *captureSUT, transparent *transparentSUT, listeners *listenersSUT) *reloadSUT {
	return &reloadSUT{proxy: root, vendors: vendors, certs: certs, app: app, apis: apis, otlp: otlp, tlsIngress: tlsIngress, connect: connect, telemetry: telemetry, capture: capture, transparent: transparent, listeners: listeners}
}

// AddSteps adds the steps of reloading the config.
func (sut *reloadSUT) AddSteps(sc *godog.ScenarioContext) {
	reloadSteps(sc, sut)
}
