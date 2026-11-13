//go:build e2e

package proxy_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// overlayCommandSteps drive the overlay command and the overlays a config
// names, through files in the scenario's folder.
func overlayCommandSteps(sc *godog.ScenarioContext, p *program) {
	sc.Step(`^the file (\S+):$`, func(name string, content *godog.DocString) error {
		return p.writeFile(name, content.Content)
	})
	sc.Step(`^the file to.yaml holds from.yaml with its keys in another order$`, func() error {
		return p.writeFile("to.yaml", `paths:
  /orders:
    get:
      responses:
        "200": { description: Orders }
      parameters:
        - { in: query, name: limit, schema: { maximum: 100, type: integer } }
info: { version: "1", title: Shop }
openapi: 3.1.0
`)
	})
	sc.Step(`^the file to.yaml holds from.yaml with the title changed to null$`, func() error {
		data, err := os.ReadFile(filepath.Join(p.dir, "from.yaml"))
		if err != nil {
			return err
		}
		return p.writeFile("to.yaml", strings.Replace(string(data), "title: Shop", "title: null", 1))
	})
	sc.Step(`^standard output says "(.*)"$`, func(text string) error {
		if !strings.Contains(p.stdout.String(), text) {
			return failf("standard output does not say %q; it is:\n%s", text, p.stdout.String())
		}
		return nil
	})
	sc.Step(`^standard output does not say "(.*)"$`, func(text string) error {
		if strings.Contains(p.stdout.String(), text) {
			return failf("standard output says %q; it is:\n%s", text, p.stdout.String())
		}
		return nil
	})
	sc.Step(`^standard output is empty$`, func() error {
		if out := p.stdout.String(); out != "" {
			return failf("standard output is %q", out)
		}
		return nil
	})
	sc.Step(`^standard output is saved as (\S+)$`, func(name string) error {
		return p.writeFile(name, p.stdout.String())
	})
	sc.Step(`^a config file whose api has the spec (\S+) and the overlay (\S+)$`, func(spec, overlay string) error {
		p.stdout, p.stderr = syncBuffer{}, syncBuffer{}
		p.writeConfig(fmt.Sprintf(`version: e2e-1
listeners:
  apps:
    address: %q
exporters:
  - otlp:
      endpoint: http://127.0.0.1:4318
apis:
  shop:
    hosts: [api.shop.test]
    spec: ./%s
    overlays: [./%s]
integrations:
  - id: shop
    apis: [shop]
`, "tcp://"+p.proxyAddr, spec, overlay))
		return nil
	})
}

func (p *program) writeFile(name, content string) error {
	return os.WriteFile(filepath.Join(p.dir, name), []byte(content), 0o600) //nolint:gosec // the test chooses its own files
}
