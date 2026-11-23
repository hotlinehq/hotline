package proxy_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

// thingsSpec is a spec whose GET /v1/things takes param, or, for a path
// parameter, whose GET /v1/things/{id} does. Its 200 response has header,
// a header object written in flow style, when header is not empty.
func thingsSpec(param, headerName, header string) string {
	route := "/v1/things"
	if strings.Contains(param, "in: path") {
		route = "/v1/things/{id}"
	}
	params := ""
	if param != "" {
		params = "      parameters:\n        - " + param + "\n"
	}
	headers := ""
	if header != "" {
		headers = fmt.Sprintf("          headers:\n            %s: %s\n", headerName, header)
	}
	return fmt.Sprintf(`openapi: 3.2.0
paths:
  %s:
    get:
%s      responses:
        "200":
          description: found
%s`, route, params, headers)
}

func parameterSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the things spec with the parameter (.+)$`, func(param string) error {
		w.useSpec(thingsSpec(param, "", ""))
		return nil
	})
	sc.Step(`^the things spec whose 200 response has the header (\S+) with (.+)$`, func(name, header string) error {
		w.useSpec(thingsSpec("", name, header))
		return nil
	})
	sc.Step(`^a call carries the header (\S+) with the value (.+)$`, func(name, value string) error {
		return w.defaultCall(name + ": " + value)
	})
	sc.Step(`^the vendor answers with the header (\S+) holding (.+)$`, func(name, value string) error {
		w.vendor(hostOf(w.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set(name, value)
		})
		return w.defaultCall()
	})
	sc.Step(`^a spec with the servers (\S+) and (\S+), and the route (\S+)$`, func(first, second, route string) error {
		w.useSpec(fmt.Sprintf("openapi: 3.2.0\nservers:\n  - url: %s\n  - url: %s\npaths:\n  %s:\n    get: {}\n", first, second, route))
		return nil
	})
	sc.Step(`^a spec with the server (\S+), whose path (\S+) has the server (\S+)$`, func(top, route, own string) error {
		w.useSpec(fmt.Sprintf("openapi: 3.2.0\nservers:\n  - url: %s\npaths:\n  %s:\n    servers:\n      - url: %s\n    get: {}\n", top, route, own))
		return nil
	})
	sc.Step(`^a spec whose path (\S+) has the server (\S+), and whose POST on it has the server (\S+)$`, func(route, path, post string) error {
		w.useSpec(fmt.Sprintf("openapi: 3.2.0\npaths:\n  %s:\n    servers:\n      - url: %s\n    get: {}\n    post:\n      servers:\n        - url: %s\n", route, path, post))
		return nil
	})
	sc.Step(`^a spec whose path (\S+) has the server (\S+), and whose COPY on it has the server (\S+)$`, func(route, path, own string) error {
		w.useSpec(fmt.Sprintf("openapi: 3.2.0\npaths:\n  %s:\n    servers:\n      - url: %s\n    additionalOperations:\n      COPY:\n        servers:\n          - url: %s\n", route, path, own))
		return nil
	})
}
