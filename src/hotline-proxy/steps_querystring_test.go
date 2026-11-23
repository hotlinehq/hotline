package proxy_test

import (
	"fmt"

	"github.com/cucumber/godog"
)

// querystringSpec is a spec whose /v1/things takes the whole query string,
// read as mediaType, with schema; extra is added to the spec.
func querystringSpec(mediaType, schema, extra string) string {
	return fmt.Sprintf(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - name: search
          in: querystring
          required: true
          content:
            %s:
              schema: %s
      responses:
        "200": { description: found }
%s`, mediaType, schema, extra)
}

const formQuery = "{ type: object, required: [q], additionalProperties: false, properties: { q: { type: string }, limit: { type: integer } } }"

// cookieSpec is a spec whose GET /v1/things takes the cookie param.
func cookieSpec(param string) string {
	return fmt.Sprintf(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - %s
      responses:
        "200": { description: found }
`, param)
}

func querystringSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the querystring spec with form content$`, func() error {
		w.useSpec(querystringSpec("application/x-www-form-urlencoded", formQuery, ""))
		return nil
	})
	sc.Step(`^the querystring spec with form content and a login key named key in the query$`, func() error {
		w.useSpec(querystringSpec("application/x-www-form-urlencoded", formQuery, `components:
  securitySchemes:
    query_key: { type: apiKey, in: query, name: key }
`))
		return nil
	})
	sc.Step(`^the querystring spec with json content$`, func() error {
		w.useSpec(querystringSpec("application/json", "{ type: object, properties: { limit: { type: integer } } }", ""))
		return nil
	})
	sc.Step(`^the querystring spec with text content$`, func() error {
		w.useSpec(querystringSpec("text/plain", "{ type: string, maxLength: 3 }", ""))
		return nil
	})
	sc.Step(`^a querystring spec with (.+)$`, func(shape string) error {
		param := map[string]string{
			"two querystring parameters":  "{ name: a, in: querystring, content: { application/json: {} } }\n        - { name: b, in: querystring, content: { application/json: {} } }",
			"a query parameter beside it": "{ name: a, in: querystring, content: { application/json: {} } }\n        - { name: limit, in: query }",
			"no content":                  "{ name: a, in: querystring, schema: { type: object } }",
			"a redact mark":               "{ name: a, in: querystring, x-hotline-redact: false, content: { application/json: {} } }",
			"two media types":             "{ name: a, in: querystring, content: { application/json: {}, text/plain: {} } }",
		}[shape]
		w.useSpec(cookieSpec(param))
		return nil
	})
	sc.Step(`^the cookie spec with theme of (style form|style cookie|no style)$`, func(style string) error {
		written := map[string]string{"style form": "style: form, ", "style cookie": "style: cookie, ", "no style": ""}[style]
		w.useSpec(cookieSpec(fmt.Sprintf("{ name: theme, in: cookie, %sschema: { enum: [dark mode] } }", written)))
		return nil
	})
	sc.Step(`^the cookie spec with ids as (an exploded list|a list not exploded)$`, func(how string) error {
		explode := how == "an exploded list"
		w.useSpec(cookieSpec(fmt.Sprintf("{ name: ids, in: cookie, explode: %t, schema: { type: array, items: { type: integer } } }", explode)))
		return nil
	})
	sc.Step(`^the cookie spec with prefs as an exploded object with no properties$`, func() error {
		w.useSpec(cookieSpec("{ name: prefs, in: cookie, schema: { type: object, additionalProperties: { type: integer } } }"))
		return nil
	})
	sc.Step(`^the cookie spec with prefs as (an exploded object|an object not exploded)$`, func(how string) error {
		explode := how == "an exploded object"
		w.useSpec(cookieSpec(fmt.Sprintf("{ name: prefs, in: cookie, explode: %t, schema: { type: object, additionalProperties: false, properties: { size: { type: integer } } } }", explode)))
		return nil
	})
}
