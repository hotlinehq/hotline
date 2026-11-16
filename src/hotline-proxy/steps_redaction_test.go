package proxy_test

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

func redactionSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the (Talon.One|Stripe) spec$`, func(vendor string) error {
		if vendor == "Stripe" {
			w.addIntegration(integration("talon-shop-a", w.stripeAPI("")))
			w.method, w.target = "GET", "https://api.stripe.com/v1/payment_intents"
			return nil
		}
		w.addIntegration(integration("talon-shop-a", w.talonAPI("")))
		return nil
	})
	sc.Step(`^an app calls "([^"]+)"$`, func(address string) error {
		method := "GET"
		if strings.Contains(address, "api.talon.one") {
			method = "PUT"
		}
		return w.call(method, address)
	})
	sc.Step(`^url.full is "([^"]+)"$`, func(full string) error {
		return w.lastHas("url.full", full)
	})
	sc.Step(`^the query in url.full is "([^"]+)"$`, func(query string) error {
		record, err := w.lastRecord()
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
		w.useSpec(`openapi: 3.1.0
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
		return w.call(w.method, w.target+"?"+query)
	})
	sc.Step(`^url.full has no (user name or password|fragment)$`, func(what string) error {
		record, err := w.lastRecord()
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
		record, err := w.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, "url.full", "url.template")
	})
}

// useSpec gives integration talon-shop-a an api for api.x.com with the spec,
// and makes GET https://api.x.com/v1/things the usual call.
func (w *world) useSpec(spec string) {
	w.addIntegration(integration("talon-shop-a", w.specAPI("api.x.com", spec, "")))
	w.method, w.target = "GET", "https://api.x.com/v1/things"
}

func headerSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a call carries (?:the header )?"([^:]+): ([^"]*)"$`, func(name, value string) error {
		if isReplyHeader(name) {
			w.vendor(hostOf(w.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
				rw.Header().Add(name, value)
			})
			return w.defaultCall()
		}
		if strings.EqualFold(name, "Content-Length") {
			return w.send(request{
				method:  w.method,
				target:  w.target,
				headers: []string{basic(w.appID + ":x")},
				body:    strings.NewReader(strings.Repeat("b", 24)),
				length:  24,
			})
		}
		return w.defaultCall(name + ": " + value)
	})
	sc.Step(`^the record has "(\S+) = (.+)"$`, func(key, value string) error {
		return w.lastHas(key, []string{value})
	})
	sc.Step(`^a spec with the header X-Debug-Token marked x-hotline-redact true$`, func() error {
		w.useSpec(`openapi: 3.1.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: X-Debug-Token, in: header, x-hotline-redact: true }
`)
		return nil
	})
	sc.Step(`^the record has no attribute for (\S+)$`, func(name string) error {
		record, err := w.lastRecord()
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
	sc.Step(`^a reply carries "([^"]+)" twice$`, func(name string) error {
		w.vendor(hostOf(w.target)).setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Add(name, "100;w=60")
			rw.Header().Add(name, "1000;w=3600")
		})
		return w.defaultCall()
	})
	sc.Step(`^the record has both values as a list$`, func() error {
		return w.lastHas("http.response.header.ratelimit-policy", []string{"100;w=60", "1000;w=3600"})
	})
	sc.Step(`^a spec that marks Authorization x-hotline-redact false$`, func() error {
		w.useSpec(`openapi: 3.1.0
paths:
  /v1/things:
    get:
      parameters:
        - { name: Authorization, in: header, x-hotline-redact: false }
`)
		return nil
	})
	sc.Step(`^a spec whose security section names X-Api-Key, also marked x-hotline-redact false$`, func() error {
		w.useSpec(`openapi: 3.1.0
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
		record, err := w.lastRecord()
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

// isReplyHeader says whether a header in a scenario comes from the vendor.
func isReplyHeader(name string) bool {
	switch strings.ToLower(name) {
	case "date", "retry-after", "request-id", "x-request-id", "set-cookie":
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), "ratelimit-")
}
