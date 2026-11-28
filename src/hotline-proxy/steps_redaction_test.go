package proxy_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

func redactionSteps(sc *godog.ScenarioContext, sut *recordContentSUT) {
	sc.Step(`^the (Talon.One|Stripe) spec$`, func(vendor string) error {
		if vendor == "Stripe" {
			sut.apis.addIntegration(integration("talon-shop-a", sut.apis.stripeAPI("")))
			sut.app.method, sut.app.target = "GET", "https://api.stripe.com/v1/payment_intents"
			return nil
		}
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.talonAPI("")))
		return nil
	})
	sc.Step(`^an app calls "([^"]+)"$`, func(address string) error {
		method := "GET"
		if strings.Contains(address, "api.talon.one") {
			method = "PUT"
		}
		return sut.app.call(method, address)
	})
	sc.Step(`^url.full is "([^"]+)"$`, func(full string) error {
		return sut.otlp.lastHas("url.full", full)
	})
	sc.Step(`^the query in url.full is "([^"]+)"$`, func(query string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		_, got, _ := strings.Cut(fmt.Sprint(record["url.full"]), "?")
		if got != query {
			return failf("the query in url.full %v is %q, want %q", record["url.full"], got, query)
		}
		return nil
	})
	sc.Step(`^a spec whose security section names the query parameter key, which is also marked x-hotline-redact false$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: key, in: query, x-hotline-redact: false }
components:
  securitySchemes:
    query_key: { type: apiKey, in: query, name: key }
`)
		return nil
	})
	sc.Step(`^an app calls with "\?([^"]+)"$`, func(query string) error {
		return sut.app.call(sut.app.method, sut.app.target+"?"+query)
	})
	sc.Step(`^url.full has no (user name or password|fragment)$`, func(what string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		full := fmt.Sprint(record["url.full"])
		if strings.ContainsAny(full, "@#") || strings.Contains(full, "user") || strings.Contains(full, "top") {
			return failf("url.full %s still has a %s", full, what)
		}
		return hasAll(record, "url.full")
	})
	sc.Step(`^there is no url.full or url.template$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.full", "url.template")
	})
}

// useSpec gives integration talon-shop-a an api for api.x.com with the spec,
// and makes GET https://api.x.com/v1/things the usual call.
func (sut *apisSUT) useSpec(spec string) {
	sut.addIntegration(integration("talon-shop-a", sut.specAPI("api.x.com", spec, "")))
	sut.app.method, sut.app.target = "GET", "https://api.x.com/v1/things"
}

func headerSteps(sc *godog.ScenarioContext, sut *recordContentSUT) {
	sc.Step(`^a call carries (?:the header )?"([^:]+): ([^"]*)"$`, func(name, value string) error {
		if isResponseHeader(name) {
			sut.vendors.vendor(hostOf(sut.app.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
				rw.Header().Add(name, value)
			})
			return sut.app.defaultCall()
		}
		if strings.EqualFold(name, "Content-Length") {
			return sut.app.send(request{
				method:  sut.app.method,
				target:  sut.app.target,
				headers: []string{basic(sut.app.appID + ":x")},
				body:    strings.NewReader(strings.Repeat("b", 24)),
				length:  24,
			})
		}
		return sut.app.defaultCall(name + ": " + value)
	})
	sc.Step(`^the record has "(\S+) = (.+)"$`, func(key, value string) error {
		return sut.otlp.lastHas(key, []string{value})
	})
	sc.Step(`^a spec with the header X-Debug-Token marked x-hotline-redact true$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: X-Debug-Token, in: header, x-hotline-redact: true }
`)
		return nil
	})
	sc.Step(`^a spec whose header X-Debug-Token is a \$ref to a parameter marked x-hotline-redact true$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - $ref: '#/components/parameters/DebugToken'
components:
  parameters:
    DebugToken: { name: X-Debug-Token, in: header, x-hotline-redact: true }
`)
		return nil
	})
	sc.Step(`^a spec whose response header Request-Id is a \$ref to a header marked x-hotline-redact true$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      responses:
        "200":
          description: ok
          headers:
            Request-Id: { $ref: '#/components/headers/RequestId' }
components:
  headers:
    RequestId: { schema: { type: string }, x-hotline-redact: true }
`)
		return nil
	})
	sc.Step(`^a response carries "([^:]+): ([^"]*)"$`, func(name, value string) error {
		sut.vendors.vendor(hostOf(sut.app.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set(name, value)
		})
		return sut.app.defaultCall()
	})
	sc.Step(`^the record has no attribute for (\S+)$`, func(name string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		for key := range record {
			if strings.HasSuffix(key, "."+strings.ToLower(name)) {
				return failf("the record has %s", key)
			}
		}
		return nil
	})
	sc.Step(`^a response carries Request-Id with the byte FF in it, which is not UTF-8$`, func() error {
		sut.vendors.vendor(hostOf(sut.app.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Request-Id", "req\xff1")
		})
		return sut.app.defaultCall()
	})
	sc.Step(`^a response carries "([^"]+)" twice$`, func(name string) error {
		sut.vendors.vendor(hostOf(sut.app.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Add(name, "100;w=60")
			rw.Header().Add(name, "1000;w=3600")
		})
		return sut.app.defaultCall()
	})
	sc.Step(`^the record has both values as a list$`, func() error {
		return sut.otlp.lastHas("http.response.header.ratelimit-policy", []string{"100;w=60", "1000;w=3600"})
	})
	sc.Step(`^a spec that marks Authorization x-hotline-redact false$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: Authorization, in: header, x-hotline-redact: false }
`)
		return nil
	})
	sc.Step(`^a spec whose security section names X-Api-Key, also marked x-hotline-redact false$`, func() error {
		sut.apis.useSpec(`openapi: 3.2.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: X-Api-Key, in: header, x-hotline-redact: false }
components:
  securitySchemes:
    api_key: { type: apiKey, in: header, name: X-Api-Key }
`)
		return nil
	})
	sc.Step(`^the record has no value for it$`, func() error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		for key, value := range record {
			if strings.HasSuffix(key, "hash") || key == "hotline.batch.id" {
				// A hash is random-looking hex, which can hold "k1"
				// by chance, now that each record has a new span id.
				continue
			}
			if strings.Contains(key, "authorization") || strings.Contains(key, "cookie") ||
				strings.Contains(key, "api-key") || strings.Contains(fmt.Sprint(value), "sid=1") ||
				strings.Contains(fmt.Sprint(value), "sk_1") || strings.Contains(fmt.Sprint(value), "k1") {
				return failf("the record has %s = %v", key, value)
			}
		}
		return nil
	})
}

// isResponseHeader says whether a header in a scenario comes from the vendor.
func isResponseHeader(name string) bool {
	switch strings.ToLower(name) {
	case "date", "retry-after", "request-id", "x-request-id", "set-cookie":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "ratelimit-")
}
