package proxy_test

import "github.com/cucumber/godog"

func routeSteps(sc *godog.ScenarioContext, sut *requestChecksSUT) {
	sc.Step(`^the S3-like routes spec$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /:
    get:
      parameters:
        - { name: list-type, in: query, schema: { type: string } }
  /#?list-type=2:
    get:
      parameters:
        - { name: prefix, in: query, schema: { type: string } }
  /#?delete:
    post: {}
  /{Key+}:
    get:
      parameters:
        - { name: Key, in: path, required: true, schema: { type: string } }
    put: {}
  /{Key+}#?tagging:
    get: {}
  /{Key+}#?uploadId:
    put:
      parameters:
        - { name: partNumber, in: query, required: true, schema: { type: integer } }
  /{Key+}#?select&select-type=2:
    post: {}
  /reports/{Key+}:
    get:
      parameters:
        - { name: Key, in: path, required: true, schema: { type: string, pattern: '^[0-9]{4}/' } }
`)
		return nil
	})
}
