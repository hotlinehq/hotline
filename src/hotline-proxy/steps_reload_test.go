package proxy_test

import (
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// noPathsSpec is a valid spec with no routes, so calls get no url.template.
const noPathsSpec = `openapi: 3.1.0
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

func reloadSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a config with integration talon-shop-a for Talon\.One$`, func() error {
		w.vendor("api.talon.one")
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		return nil
	})
	sc.Step(`^an app calls Talon\.One as (\S+)$`, func(id string) error {
		if err := w.start(); err != nil {
			return err
		}
		w.appID = id
		return w.defaultCall()
	})
	sc.Step(`^the config file changes so that ([^,]+)$`, func(change string) error {
		return w.changeConfig(change)
	})
	sc.Step(`^the config file changes so that ([^,]+), and talon-shop-b also uses Talon\.One$`, func(change string) error {
		w.addIntegration(integration("talon-shop-b", w.talonAPI("")))
		return w.changeConfig(change)
	})
	sc.Step(`^the Talon\.One spec file changes so that it has no paths$`, func() error {
		w.writeFile("specs/talon-one.openapi.yaml", noPathsSpec)
		return nil
	})
	sc.Step(`^the overlay file no-paths\.yaml arrives, removing every path$`, func() error {
		w.writeFile("overlays/no-paths.yaml", noPathsOverlay)
		return nil
	})
	sc.Step(`^talon-shop-a's fingerprintKey is read from a file$`, func() error {
		file := w.writeFile("keys/talon-shop-a", signingKey+"\n")
		w.integrations[0] += "    fingerprintKey: { file: " + file + " }\n"
		return nil
	})
	sc.Step(`^talon-shop-a's key file changes to another key$`, func() error {
		w.replaceFile("keys/talon-shop-a", "another-key-for-talon-shop-a\n")
		return nil
	})
	sc.Step(`^the proxy checks its files$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.proxy.CheckFiles()
		return nil
	})
	sc.Step(`^the proxy is told to reload$`, func() error {
		w.proxy.Reload()
		return nil
	})

	sc.Step(`^record (\d+) has no url\.template$`, func(n int) error {
		record, err := w.nth(n)
		if err != nil {
			return err
		}
		return hasNo(record, "url.template")
	})
	sc.Step(`^record (\d+) has url\.template (\S+)$`, func(n int, template string) error {
		record, err := w.nth(n)
		if err != nil {
			return err
		}
		return has(record, "url.template", template)
	})
	sc.Step(`^record (\d+) and record (\d+) carry (the same|different) (\S+)$`, func(a, b int, same, key string) error {
		first, err := w.nth(a)
		if err != nil {
			return err
		}
		second, err := w.nth(b)
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
		if strings.Contains(w.logs.String(), `msg="config reloaded"`) != (logs == "logs") {
			return failf("the proxy %s that the config was reloaded; the log is %s", logs, w.logs.String())
		}
		return nil
	})
	sc.Step(`^the proxy does not log that a new TLS certificate is in use$`, func() error {
		if strings.Contains(w.logs.String(), "new TLS certificate in use") {
			return failf("the proxy logged a new TLS certificate; the log is %s", w.logs.String())
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that the reload failed, naming (.+)$`, func(name string) error {
		return w.warned("config reload failed", name)
	})
	sc.Step(`^the proxy logs a warning that (\S+) needs a restart$`, func(setting string) error {
		return w.warned(setting, "needs a restart")
	})
}

// changeConfig rewrites the config file with one change.
func (w *world) changeConfig(change string) error {
	if err := w.start(); err != nil {
		return err
	}
	switch change {
	case "talon-shop-b also uses Talon.One":
		w.addIntegration(integration("talon-shop-b", w.talonAPI("")))
	case "it is not valid YAML":
		w.writeFile("hotline-proxy.yaml", "listen: [proxy\n")
		return nil
	case "talon-shop-a's spec file is missing":
		w.apis[0].yaml = strings.Replace(w.apis[0].yaml, "talon-one.openapi.yaml", "missing.yaml", 1)
	case "Talon.One has the overlay no-paths.yaml":
		w.apis[0].yaml += "    overlays: [./overlays/no-paths.yaml]\n"
	case "listen.proxy moves to another port":
		addr := w.addr
		w.addr = freePort()
		defer func() { w.addr = addr }()
	case "listen.admin moves to another port":
		w.adminAddr = freePort()
	case "there is one more exporter":
		w.downEndpoints = append(w.downEndpoints, freePort())
	case "listen.tls is added":
		cert := w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		w.writeKeyPair(cert, cert)
		w.listenTLS = "  tls: { cert: ./tls/cert.pem, key: ./tls/key.pem }\n"
	case "the telemetry section turns off go.*":
		w.telemetryDisable = append(w.telemetryDisable, "go.*")
	case "telemetry.pprof is turned on":
		w.telemetryPprof = true
	case "listen.connect is added":
		w.useInterceptionIssuer(newIssuer())
	default:
		return godog.ErrPending
	}
	w.writeFile("hotline-proxy.yaml", w.config())
	return nil
}

// nth is the scenario's record number n, counted from 1, once the proxy
// has sent everything.
func (w *world) nth(n int) (map[string]any, error) {
	records := w.records()
	if n > len(records) {
		return nil, failf("there are %d records, not %d", len(records), n)
	}
	return records[n-1].attrs, nil
}

// warned checks that one warning in the log holds every part.
func (w *world) warned(parts ...string) error {
	for _, line := range strings.Split(w.logs.String(), "\n") {
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
	return failf("no warning says %q; the log is %s", parts, w.logs.String())
}
