package proxy_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"
)

func overlaySteps(sc *godog.ScenarioContext, sut *requestChecksSUT) {
	sc.Step(`^the overlay (\S+):$`, func(name string, content *godog.DocString) error {
		sut.proxy.writeFile("specs/"+name, content.Content)
		return nil
	})
	sc.Step(`^the shop spec with the overlays? (.+)$`, func(names string) error {
		var paths []string
		for _, name := range strings.Split(names, ", ") {
			paths = append(paths, "./specs/"+name)
		}
		return sut.apis.shopSpec("talon-shop-a", "", "    overlays: ["+strings.Join(paths, ", ")+"]\n")
	})
	sc.Step(`^the integration (\S+) with the same shop spec and no overlay$`, func(id string) error {
		return sut.apis.shopSpec(id, "shop-plain", "")
	})
	sc.Step(`^the shop spec for (\S+) with "([^"]*)" on its entry for the api$`, func(id, setting string) error {
		line, err := sut.apis.shopAPI("", "")
		name := strings.TrimSpace(strings.TrimPrefix(line, "      - "))
		sut.apis.addIntegration(integration(id, "      - { api: "+name+", "+setting+" }\n"))
		return err
	})
	sc.Step(`^the shop spec file holds "([^"]*)"$`, func(content string) error {
		sut.proxy.writeFile("specs/shop.openapi.yaml", content)
		return nil
	})
	sc.Step(`^the shop spec file says openapi (\S+)$`, func(version string) error {
		spec, err := os.ReadFile(filepath.Join(sut.proxy.dir, "specs", "shop.openapi.yaml"))
		sut.proxy.writeFile("specs/shop.openapi.yaml", regexp.MustCompile(`(?m)^openapi: .*$`).ReplaceAllString(string(spec), "openapi: "+version))
		return err
	})
	sc.Step(`^(\S+) sends GET (\S+)$`, func(id, path string) error {
		sut.app.appID = id
		return sut.app.shopCall("GET", path, true, "", "", "")
	})
	sc.Step(`^the proxy logged a warning with ((?:"[^"]*"(?:, | and )?)+)$`, func(words string) error {
		if sut.proxy.startErr != nil {
			return sut.proxy.startErr
		}
		want := quoted(words)
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && containsAll(line, want) {
				return nil
			}
		}
		return failf("no warning names all of %q; the log is %s", want, sut.proxy.logs.String())
	})
	sc.Step(`^the config is refused with a message naming ((?:"[^"]*"(?:, | and )?)+)$`, func(words string) error {
		return sut.proxy.refusedWith(quoted(words)...)
	})
}

// shopSpec adds the integration id with the shop api.
func (sut *apisSUT) shopSpec(id, name, extra string) error {
	line, err := sut.shopAPI(name, extra)
	sut.addIntegration(integration(id, line))
	return err
}

// shopAPI adds an api for api.shop.test with the shop spec and extra
// settings, named name (or the host, when name is empty), and returns the
// line an integration names it with. Every api made this way reads the
// same spec file.
func (sut *apisSUT) shopAPI(name, extra string) (string, error) {
	spec, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
	sut.proxy.writeFile("specs/shop.openapi.yaml", string(spec))
	if name == "" {
		name = "api.shop.test"
	}
	return sut.api(name, "    hosts: [api.shop.test]\n    spec: ./specs/shop.openapi.yaml\n"+extra), err
}

// quoted is the list of words written as "a", "b" and "c".
func quoted(words string) []string {
	var out []string
	for i, part := range strings.Split(words, `"`) {
		if i%2 == 1 {
			out = append(out, part)
		}
	}
	return out
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
