package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func checksSteps(sc *godog.ScenarioContext, sut *requestChecksSUT) {
	sc.Step(`^the shop spec$`, func() error {
		spec, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
		if err != nil {
			return err
		}
		sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI("api.shop.test", string(spec), "")))
		return nil
	})
	sc.Step(`^the shop spec with checks (\[[^\]]*\])$`, func(list string) error {
		return sut.shopSpecChecks(list)
	})
	sc.Step(`^the shop spec with the unknown check "([^"]*)"$`, func(name string) error {
		sut.proxy.problem = "unknown check " + `"` + name + `"`
		return sut.shopSpecChecks("[path, " + name + "]")
	})
	sc.Step(`^the record has no request or response results$`, func() error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		if err := hasNo(record, resultsKey("request")); err != nil {
			return err
		}
		return hasNo(record, resultsKey("response"))
	})
	sc.Step(`^the vendor refuses the connection$`, func() error {
		sut.vendors.refuse("api.shop.test", "443")
		return nil
	})
	sc.Step(`^the vendor answers (\d+)(?: with "([^"]*)")?(?: and the (json )?body (.*))?$`, func(status int, headers, json, body string) error {
		sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			// Read the whole request first, as a real vendor does: an answer
			// sent before the body has arrived can cut the body short.
			_, _ = io.Copy(io.Discard, r.Body)
			if json != "" {
				rw.Header().Set("Content-Type", "application/json")
			}
			for _, h := range splitHeaders(headers) {
				name, value, _ := strings.Cut(h, ": ")
				rw.Header().Set(name, value)
			}
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(body))
		})
		return nil
	})
	sc.Step(`^the app sends (GET|POST|PUT|DELETE) (\S+)( without the login key)?(?: with "([^"]*)")?(?: and the (json|form|pdf) body (.*))?$`,
		func(method, path, noKey, headers, kind, body string) error {
			return sut.app.shopCall(method, path, noKey == "", headers, kind, body)
		})
	sc.Step(`^the app sends POST /orders with "([^"]*)" and a json body of (\d+) MiB$`, func(headers string, mib int) error {
		pad := strings.Repeat("x", mib<<20)
		return sut.app.shopCall("POST", "/orders", true, headers, "json", `{"item":"`+pad+`","quantity":1}`)
	})
	sc.Step(`^the app sends POST /payments and a form body whose metadata field is (\d+) names deep$`, func(names int) error {
		body := "amount=5&currency=eur&metadata" + strings.Repeat("[a]", names-1) + "=1"
		return sut.app.shopCall("POST", "/payments", true, "", "form", body)
	})
	sc.Step(`^the app sends POST /orders with "([^"]*)" and an order with (\d+) lines without a sku$`, func(headers string, n int) error {
		lines := strings.TrimSuffix(strings.Repeat(`{},`, n), ",")
		return sut.app.shopCall("POST", "/orders", true, headers, "json", `{"item":"A1","quantity":1,"lines":[`+lines+`]}`)
	})
	sc.Step(`^the app sends no integration ID$`, func() error {
		sut.app.appID = ""
		return nil
	})
	sc.Step(`^the app got the vendor's answer$`, func() error {
		if sut.app.resp == nil || sut.app.resp.StatusCode != http.StatusCreated {
			return failf("the app got %v, want the vendor's 201", sut.app.resp)
		}
		return nil
	})
	sc.Step(`^the record has no (request|response) results$`, func(side string) error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return hasNo(record, resultsKey(side))
	})
	sc.Step(`^the (request|response) results are (.+)$`, func(side, want string) error {
		got, err := sut.otlp.checkResults(side)
		if err != nil {
			return err
		}
		if strings.Join(got, "; ") != want {
			return failf("the %s results are %q, want %q", side, strings.Join(got, "; "), want)
		}
		return nil
	})
	sc.Step(`^the record has (\d+) request results$`, func(n int) error {
		got, err := sut.otlp.checkResults("request")
		if err != nil {
			return err
		}
		if len(got) != n {
			return failf("the record has %d request results, want %d: %v", len(got), n, got)
		}
		return nil
	})
	sc.Step(`^no result holds (\S+) or (\S+)$`, func(first, second string) error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		all := fmt.Sprint(record["hotline.schema.request.errors"], record["hotline.schema.response.errors"])
		if !strings.Contains(all, "query") {
			return failf("the record has no request results to look at")
		}
		if strings.Contains(all, first) || strings.Contains(all, second) {
			return failf("the results %s hold a value from the call", all)
		}
		return nil
	})
	sc.Step(`^the record says (hotline\.schema\.[a-z_.]+)=(\S+)$`, func(key, value string) error {
		return sut.otlp.lastHas(key, value)
	})
	sc.Step(`^the record has no (hotline\.schema\.[a-z_.]+)$`, func(key string) error {
		record, err := sut.otlp.record()
		if err != nil {
			return err
		}
		return hasNo(record, key)
	})
}

// shopCall sends a call to the shop vendor, with its login key unless the
// scenario leaves it out.
func (sut *appSUT) shopCall(method, path string, withKey bool, headers, kind, body string) error {
	var lines []string
	if sut.appID != "" {
		lines = append(lines, basic(sut.appID+":x"))
	}
	if withKey {
		lines = append(lines, "Authorization: Bearer t0k3n")
	}
	lines = append(lines, splitHeaders(headers)...)
	r := request{method: method, target: "https://api.shop.test" + path, length: -1}
	if kind != "" {
		lines = append(lines, "Content-Type: "+map[string]string{
			"json": "application/json",
			"form": "application/x-www-form-urlencoded",
			"pdf":  "application/pdf",
		}[kind])
		r.body, r.length = strings.NewReader(body), len(body)
	}
	r.headers = lines
	return sut.send(r)
}

func splitHeaders(headers string) []string {
	if headers == "" {
		return nil
	}
	return strings.Split(headers, "; ")
}

func resultsKey(side string) string {
	return map[string]string{
		"request":  "hotline.schema.request.errors",
		"response": "hotline.schema.response.errors",
	}[side]
}

// resultsOf are a record's results on one side, each written as
// "part place rule expected".
func resultsOf(record map[string]any, side string) ([]string, error) {
	list, ok := record[resultsKey(side)].([]any)
	if !ok {
		return nil, failf("the record has no list of %s results; it has %v", side, record)
	}
	var got []string
	for _, item := range list {
		r, ok := item.(map[string]any)
		if !ok {
			return nil, failf("a %s result is %v, not a structured entry", side, item)
		}
		var parts []string
		for _, key := range []string{"part", "place", "rule", "expected"} {
			if v, found := r[key]; found {
				parts = append(parts, fmt.Sprint(v))
			}
		}
		got = append(got, strings.Join(parts, " "))
	}
	return got, nil
}

// shopSpecChecks adds the shop api with a checks list, written as in the
// config, such as [path, query].
func (sut *requestChecksSUT) shopSpecChecks(list string) error {
	spec, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
	if err != nil {
		return err
	}
	// A feature's background may have added the shop api without checks.
	sut.apis.removeIntegrationsAndAPIs()
	sut.apis.addIntegration(integration("talon-shop-a", sut.apis.specAPI("api.shop.test", string(spec), "    checks: "+list+"\n")))
	return nil
}

// requestChecksSUT is the checks of calls against the vendor's spec: what
// the proxy finds wrong in a request or response, by method, route, query,
// parameter and body.
type requestChecksSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	app     *appSUT
	apis    *apisSUT
	otlp    *otlpSUT
	aws     *awsSUT
	// parameter is the spec parameter a scenario is about.
	parameter string
}

func newRequestChecksSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT, apis *apisSUT, otlp *otlpSUT, aws *awsSUT) *requestChecksSUT {
	return &requestChecksSUT{proxy: root, vendors: vendors, app: app, apis: apis, otlp: otlp, aws: aws}
}

// AddSteps adds the steps of the request and response checks.
func (sut *requestChecksSUT) AddSteps(sc *godog.ScenarioContext) {
	checksSteps(sc, sut)
	specSteps(sc, sut)
	methodsSteps(sc, sut)
	querystringSteps(sc, sut)
	parameterSteps(sc, sut)
	discriminatorSteps(sc, sut)
	routeSteps(sc, sut)
	bodyRouteSteps(sc, sut)
	xmlSteps(sc, sut)
	overlaySteps(sc, sut)
}
