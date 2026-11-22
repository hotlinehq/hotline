package proxy_test

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

func methodsSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the methods spec$`, func() error {
		w.useSpec(`openapi: 3.2.0
paths:
  /v1/search:
    query:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [filter]
              properties:
                filter: { type: string }
      responses:
        "200": { description: found }
  /v1/pets/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: integer } }
    get:
      responses:
        "200": { description: a pet }
    additionalOperations:
      COPY:
        responses:
          "200": { description: copied }
`)
		return nil
	})
	sc.Step(`^a call is (\S+) (/\S*) with the json body (.*)$`, func(method, path, body string) error {
		r := request{method: method, target: "https://" + hostOf(w.target) + path, length: len(body),
			headers: []string{"Content-Type: application/json"}, body: strings.NewReader(body)}
		if w.appID != "" {
			r.headers = append([]string{basic(w.appID + ":x")}, r.headers...)
		}
		return w.send(r)
	})
	sc.Step(`^a spec whose path (\S+) lists "?([^"]*?)"? under additionalOperations$`, func(path, key string) error {
		w.useSpec(fmt.Sprintf("openapi: 3.2.0\npaths:\n  %s:\n    additionalOperations:\n      %q: {}\n", path, key))
		return nil
	})
}
