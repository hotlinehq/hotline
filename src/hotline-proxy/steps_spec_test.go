package proxy_test

import (
	"fmt"
	"net/http"

	"github.com/cucumber/godog"
)

func specSteps(sc *godog.ScenarioContext, sut *requestChecksSUT) {
	sc.Step(`^a call is ([A-Za-z]+) (/\S*)$`, func(method, path string) error {
		return sut.app.call(method, "https://"+hostOf(sut.app.target)+path)
	})
	sc.Step(`^the route template is (\S+)$`, func(template string) error {
		return sut.otlp.lastHas("url.template", template)
	})
	sc.Step(`^url.template is (\S+)$`, func(template string) error {
		return sut.otlp.lastHas("url.template", template)
	})
	sc.Step(`^there is no route(?:, because the spec only lists PUT)?$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.template")
	})
	sc.Step(`^a spec with the route (\S+)$`, func(route string) error {
		sut.apis.useSpec(fmt.Sprintf("openapi: 3.2.0\npaths:\n  %s:\n    get: {}\n", route))
		return nil
	})
	sc.Step(`^a spec whose servers address is (\S+) with the route (\S+)$`, func(server, route string) error {
		sut.apis.useSpec(fmt.Sprintf("openapi: 3.2.0\nservers:\n  - url: \"%s\"\npaths:\n  %s:\n    get: {}\n", server, route))
		return nil
	})
	sc.Step(`^a spec saying openapi (\S+) with the route (\S+)$`, func(version, route string) error {
		sut.apis.useSpec(fmt.Sprintf("openapi: %s\npaths:\n  %s:\n    get: {}\n", version, route))
		return nil
	})
	sc.Step(`^a spec with the route (\S+), a webhook (\S+) and a callback (\S+)$`, func(route, webhook, callback string) error {
		sut.apis.useSpec(fmt.Sprintf(`openapi: 3.2.0
paths:
  %s:
    get:
      callbacks:
        done:
          "{$request.query.url}%s":
            post: { responses: { "200": { description: ok } } }
webhooks:
  %s:
    get: { responses: { "200": { description: ok } } }
`, route, callback, webhook))
		return nil
	})
	sc.Step(`^a spec with the route (\S+) that also uses query, additionalOperations, in querystring and itemSchema$`, func(route string) error {
		sut.apis.useSpec(fmt.Sprintf(`openapi: 3.2.0
paths:
  %s:
    get:
      responses:
        "200":
          description: events
          content:
            text/event-stream:
              itemSchema: { type: object, required: [data] }
    query:
      parameters:
        - name: filter
          in: querystring
          content:
            application/x-www-form-urlencoded:
              schema: { type: object }
    additionalOperations:
      LINK: {}
`, route))
		return nil
	})
	sc.Step(`^a spec with the routes (\S+) and (\S+)$`, func(first, second string) error {
		sut.apis.useSpec(fmt.Sprintf("openapi: 3.2.0\npaths:\n  %s:\n    get: {}\n  %s:\n    get: {}\n", first, second))
		return nil
	})

	sc.Step(`^a spec where (a path parameter|a query parameter|a header parameter|any parameter|a declared response header) is (marked x-hotline-redact true|marked x-hotline-redact false|not marked)$`, func(parameter, marked string) error {
		mark := map[string]string{
			"marked x-hotline-redact true":  ", x-hotline-redact: true",
			"marked x-hotline-redact false": ", x-hotline-redact: false",
			"not marked":                    "",
		}[marked]
		sut.parameter = parameter
		sut.apis.useSpec(fmt.Sprintf(`openapi: 3.2.0
paths:
  /v1/things/{thing}:
    get:
      parameters:
        - { name: thing, in: path%s }
        - { name: q, in: query%s }
        - { name: X-Param, in: header%s }
      responses:
        "200":
          description: ok
          headers:
            X-Response: { schema: { type: string }%s }
`, only(parameter, "a path parameter", mark), only(parameter, "a query parameter", mark),
			only(parameter, "a header parameter", mark), only(parameter, "a declared response header", mark)))
		return nil
	})
	sc.Step(`^the spec is read$`, func() error {
		sut.vendors.vendor("api.x.com").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("X-Response", "R-1")
		})
		return sut.app.call("GET", "https://api.x.com/v1/things/T-1?q=Q-1&key=K-1",
			"X-Param: H-1", "X-Op-Key: O-1", "Authorization: Bearer A-1", "Cookie: sid=C-1")
	})
	sc.Step(`^its value is to be kept$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		switch sut.parameter {
		case "a path parameter":
			return has(record, "url.full", "https://api.x.com/v1/things/T-1?q=REDACTED&key=REDACTED")
		case "a query parameter":
			return has(record, "url.full", "https://api.x.com/v1/things/REDACTED?q=Q-1&key=REDACTED")
		case "a header parameter":
			return has(record, "http.request.header.x-param", []string{"H-1"})
		}
		return has(record, "http.response.header.x-response", []string{"R-1"})
	})
	sc.Step(`^its name is kept and its value redacted$`, func() error {
		return sut.otlp.lastHas("http.request.header.x-param", []string{"REDACTED"})
	})
	sc.Step(`^it counts as not marked$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		if err := has(record, "url.full", "https://api.x.com/v1/things/REDACTED?q=REDACTED&key=REDACTED"); err != nil {
			return err
		}
		return hasNo(record, "http.request.header.x-param", "http.response.header.x-response")
	})
	sc.Step(`^a spec that lists the query parameter page for the whole route as not marked$`, func() error {
		return nil
	})
	sc.Step(`^lists page again for GET, marked x-hotline-redact false$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    parameters:
      - { name: page, in: query }
    get:
      parameters:
        - { name: page, in: query, x-hotline-redact: false }
`)
		return nil
	})
	sc.Step(`^the spec is read for GET$`, func() error {
		return sut.app.call("GET", "https://api.x.com/v1/things?page=2")
	})
	sc.Step(`^page is to be kept$`, func() error {
		return sut.otlp.lastHas("url.full", "https://api.x.com/v1/things?page=2")
	})
	sc.Step(`^a call sends x-debug-token$`, func() error {
		return sut.app.defaultCall("x-debug-token: abc")
	})
	sc.Step(`^the mark applies$`, func() error {
		return sut.otlp.lastHas("http.request.header.x-debug-token", []string{"REDACTED"})
	})

	sc.Step(`^a spec whose security section has (.+)$`, func(scheme string) error {
		yaml := map[string]string{ //nolint:gosec // security scheme types, not credentials
			"an http bearer scheme": "{ type: http, scheme: bearer }",
			"an http basic scheme":  "{ type: http, scheme: basic }",
			"an apiKey scheme in the Authorization header, as Talon.One has": "{ type: apiKey, in: header, name: Authorization }",
			"an apiKey scheme in the query named key":                        "{ type: apiKey, in: query, name: key }",
			"an apiKey scheme in a cookie named sid":                         "{ type: apiKey, in: cookie, name: sid }",
		}[scheme]
		sut.apis.useSpec(`openapi: 3.2.0
security:
  - auth: []
paths:
  /v1/things/{thing}:
    get:
      parameters:
        - { name: thing, in: path, x-hotline-redact: false }
        - { name: key, in: query, x-hotline-redact: false }
        - { name: Authorization, in: header, x-hotline-redact: false }
        - { name: sid, in: cookie, x-hotline-redact: false }
components:
  securitySchemes:
    auth: ` + yaml + "\n")
		return nil
	})
	sc.Step(`^(the Authorization header|the query parameter key|the cookie sid) is a secret$`, func(secret string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		switch secret {
		case "the query parameter key":
			return has(record, "url.full", "https://api.x.com/v1/things/T-1?q=REDACTED&key=REDACTED")
		case "the cookie sid":
			return sut.otlp.nowhere("C-1")
		}
		return sut.otlp.nowhere("A-1")
	})
	sc.Step(`^a spec where one operation has its own security section with an apiKey header X-Op-Key$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
security:
  - bearer: []
paths:
  /v1/things/{thing}:
    get:
      security:
        - op_key: []
      parameters:
        - { name: X-Op-Key, in: header, x-hotline-redact: false }
        - { name: Authorization, in: header, x-hotline-redact: false }
components:
  securitySchemes:
    bearer: { type: http, scheme: bearer }
    op_key: { type: apiKey, in: header, name: X-Op-Key }
`)
		return nil
	})
	sc.Step(`^X-Op-Key is a secret as well as the global ones$`, func() error {
		return sut.otlp.nowhere("O-1", "A-1")
	})
}

// only gives the mark to the parameter the scenario is about.
func only(parameter, this, mark string) string {
	if parameter == this {
		return mark
	}
	return ""
}
