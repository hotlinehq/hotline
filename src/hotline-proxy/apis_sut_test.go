package proxy_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
)

// apisSUT is the config's apis and integrations: which vendor apis the
// proxy knows, with their specs, and which integrations call them.
type apisSUT struct {
	proxy   *proxySUT
	app     *appSUT
	vendors *vendorsSUT
	// entries are the apis of the config's apis section.
	entries []namedAPI
	// integrations is the YAML of the config's integrations list.
	integrations []string
	// noIntegrations leaves the integrations out of the config; otherwise
	// a config with none gets talon-shop-a for Talon.One and Stripe.
	noIntegrations bool
}

func newApisSUT(root *proxySUT, app *appSUT, vendors *vendorsSUT) *apisSUT {
	sut := &apisSUT{proxy: root, app: app, vendors: vendors}
	root.beforeEachStart(func() {
		if len(sut.integrations) == 0 && !sut.noIntegrations {
			sut.addIntegration(integration("talon-shop-a", sut.talonAPI(""), sut.stripeAPI("")))
		}
	})
	root.addConfig(configApis, func() string {
		if len(sut.integrations) == 0 {
			return sut.apisSection()
		}
		return sut.apisSection() + "integrations:\n" + strings.Join(sut.integrations, "")
	})
	return sut
}

// addIntegration adds one integration, written as YAML, to the config.
func (sut *apisSUT) addIntegration(yaml string) {
	sut.integrations = append(sut.integrations, yaml)
}

// namedAPI is one entry of the config's apis section.
type namedAPI struct {
	name string
	yaml string
}

// api adds a vendor api, written as YAML, to the config's apis section and
// returns the line an integration uses to name it. The same YAML is added
// only once.
func (sut *apisSUT) api(name, yaml string) string {
	taken := map[string]bool{}
	for _, a := range sut.entries {
		if a.yaml == yaml {
			return "      - " + a.name + "\n"
		}
		taken[a.name] = true
	}
	base := name
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	sut.entries = append(sut.entries, namedAPI{name: name, yaml: yaml})
	return "      - " + name + "\n"
}

// talonAPI adds an api for api.talon.one with the Talon.One spec.
func (sut *apisSUT) talonAPI(extra string) string {
	sut.proxy.writeFile("specs/talon-one.openapi.yaml", sut.proxy.exampleFile("specs/talon-one.openapi.yaml"))
	return sut.api("talon-one", "    hosts: [api.talon.one]\n    spec: ./specs/talon-one.openapi.yaml\n"+extra)
}

func (sut *apisSUT) stripeAPI(extra string) string {
	sut.proxy.writeFile("specs/stripe.openapi.yaml", sut.proxy.exampleFile("specs/stripe.openapi.yaml"))
	return sut.api("stripe", "    hosts: [api.stripe.com]\n    spec: ./specs/stripe.openapi.yaml\n"+extra)
}

// specAPI adds an api for host with a spec written by the test.
func (sut *apisSUT) specAPI(host, spec, extra string) string {
	name := fmt.Sprintf("specs/%s.yaml", host)
	sut.proxy.writeFile(name, spec)
	return sut.api(host, fmt.Sprintf("    hosts: [%s]\n    spec: ./%s\n%s", host, name, extra))
}

func integration(id string, apis ...string) string {
	return fmt.Sprintf("  - id: %s\n    apis:\n%s", id, strings.Join(apis, ""))
}

// apisSection is the YAML of the config's apis section.
func (sut *apisSUT) apisSection() string {
	if len(sut.entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("apis:\n")
	for _, a := range sut.entries {
		b.WriteString("  " + a.name + ":\n" + a.yaml)
	}
	return b.String()
}

// raw is a config written by a test: its head, the apis added so far and
// the integrations.
func (sut *apisSUT) raw(head, integrations string) string {
	return head + sut.apisSection() + "integrations:\n" + integrations
}

// apiFor adds an api for host: the example specs for Talon.One and
// Stripe, a small spec for any other host.
func (sut *apisSUT) apiFor(host, extra string) string {
	switch host {
	case "api.talon.one":
		return sut.talonAPI(extra)
	case "api.stripe.com":
		return sut.stripeAPI(extra)
	}
	return sut.specAPI(host, "openapi: 3.2.0\npaths:\n  /a:\n    get: {}\n", extra)
}

// useRepositorySpec gives the app an integration whose one api is host,
// described by the spec in the repository at path, and makes the fake
// vendor answer every call with status, contentType and body.
func (sut *apisSUT) useRepositorySpec(host, path string, status int, contentType, body, extra string) error {
	spec, err := filepath.Abs(filepath.FromSlash(path))
	if err != nil {
		return err
	}
	api := sut.api(host, fmt.Sprintf("    hosts: [%s]\n    spec: %s\n", host, spec))
	sut.addIntegration(integration("talon-shop-a", api) + extra)
	sut.vendors.vendor(host).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", contentType)
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	})
	return nil
}

// integrationCount is how many integrations the config has.
func (sut *apisSUT) integrationCount() int { return len(sut.integrations) }

// onlyIntegration makes yaml the config's one integration.
func (sut *apisSUT) onlyIntegration(yaml string) { sut.integrations = []string{yaml} }

// addToLastIntegration adds lines to the integration added last.
func (sut *apisSUT) addToLastIntegration(lines string) {
	sut.integrations[len(sut.integrations)-1] += lines
}

// setIntegration puts yaml, the integration whose id is id, in the
// config: in the place of the one with that id, or after the others.
func (sut *apisSUT) setIntegration(id, yaml string) {
	at := slices.IndexFunc(sut.integrations, func(old string) bool { return strings.Contains(old, "id: "+id+"\n") })
	if at < 0 {
		sut.integrations = append(sut.integrations, yaml)
		return
	}
	sut.integrations[at] = yaml
}

// removeIntegration leaves out the integration whose id is id.
func (sut *apisSUT) removeIntegration(id string) {
	sut.integrations = slices.DeleteFunc(sut.integrations, func(yaml string) bool {
		return strings.Contains(yaml, "id: "+id+"\n")
	})
}

// removeIntegrations leaves every integration out of the config, and
// removeIntegrationsAndAPIs every api too.
func (sut *apisSUT) removeIntegrations()        { sut.integrations = nil }
func (sut *apisSUT) removeIntegrationsAndAPIs() { sut.integrations, sut.entries = nil, nil }

// leaveOutDefaultIntegrations writes the config with no integrations, not even
// the default ones.
func (sut *apisSUT) leaveOutDefaultIntegrations() { sut.noIntegrations = true }

// addToEveryAPI adds lines to every api, and addToFirstAPI to the first.
func (sut *apisSUT) addToEveryAPI(lines string) {
	for i := range sut.entries {
		sut.entries[i].yaml += lines
	}
}
func (sut *apisSUT) addToFirstAPI(lines string) { sut.entries[0].yaml += lines }

// renameFirstSpec makes the first api name the spec file to in place of
// from.
func (sut *apisSUT) renameFirstSpec(from, to string) {
	sut.entries[0].yaml = strings.Replace(sut.entries[0].yaml, from, to, 1)
}
