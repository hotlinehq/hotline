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

func checksSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the shop spec$`, func() error {
		spec, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
		if err != nil {
			return err
		}
		w.addIntegration(integration("talon-shop-a", w.specAPI("api.shop.test", string(spec), "")))
		return nil
	})
	sc.Step(`^the shop spec with checks (\[[^\]]*\])$`, func(list string) error {
		return w.shopSpecChecks(list)
	})
	sc.Step(`^the shop spec with the unknown check "([^"]*)"$`, func(name string) error {
		w.problem = "unknown check " + `"` + name + `"`
		return w.shopSpecChecks("[path, " + name + "]")
	})
	sc.Step(`^the record has no request or reply results$`, func() error {
		record, err := w.record()
		if err != nil {
			return err
		}
		if err := hasNo(record, resultsKey("request")); err != nil {
			return err
		}
		return hasNo(record, resultsKey("reply"))
	})
	sc.Step(`^the vendor refuses the connection$`, func() error {
		l := listenLocal()
		w.routeTo("api.shop.test", "443", l)
		return l.Close()
	})
	sc.Step(`^the vendor answers (\d+)(?: with "([^"]*)")?(?: and the (json )?body (.*))?$`, func(status int, headers, json, body string) error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
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
	sc.Step(`^the app sends (GET|POST|PUT) (\S+)( without the login key)?(?: with "([^"]*)")?(?: and the (json|form|pdf) body (.*))?$`,
		func(method, path, noKey, headers, kind, body string) error {
			return w.shopCall(method, path, noKey == "", headers, kind, body)
		})
	sc.Step(`^the app sends POST /orders with "([^"]*)" and a json body of (\d+) MiB$`, func(headers string, mib int) error {
		pad := strings.Repeat("x", mib<<20)
		return w.shopCall("POST", "/orders", true, headers, "json", `{"item":"`+pad+`","quantity":1}`)
	})
	sc.Step(`^the app sends POST /orders with "([^"]*)" and an order with (\d+) lines without a sku$`, func(headers string, n int) error {
		lines := strings.TrimSuffix(strings.Repeat(`{},`, n), ",")
		return w.shopCall("POST", "/orders", true, headers, "json", `{"item":"A1","quantity":1,"lines":[`+lines+`]}`)
	})
	sc.Step(`^the app sends no integration ID$`, func() error {
		w.appID = ""
		return nil
	})
	sc.Step(`^the app got the vendor's answer$`, func() error {
		if w.resp == nil || w.resp.StatusCode != http.StatusCreated {
			return failf("the app got %v, want the vendor's 201", w.resp)
		}
		return nil
	})
	sc.Step(`^the record has no (request|reply) results$`, func(side string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasNo(record, resultsKey(side))
	})
	sc.Step(`^the (request|reply) results are (.+)$`, func(side, want string) error {
		got, err := w.results(side)
		if err != nil {
			return err
		}
		if strings.Join(got, "; ") != want {
			return failf("the %s results are %q, want %q", side, strings.Join(got, "; "), want)
		}
		return nil
	})
	sc.Step(`^the record has (\d+) request results$`, func(n int) error {
		got, err := w.results("request")
		if err != nil {
			return err
		}
		if len(got) != n {
			return failf("the record has %d request results, want %d: %v", len(got), n, got)
		}
		return nil
	})
	sc.Step(`^no result holds (\S+) or (\S+)$`, func(first, second string) error {
		record, err := w.record()
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
		return w.lastHas(key, value)
	})
	sc.Step(`^the record has no (hotline\.schema\.[a-z_.]+)$`, func(key string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasNo(record, key)
	})
}

// shopCall sends a call to the shop vendor, with its login key unless the
// scenario leaves it out.
func (w *world) shopCall(method, path string, withKey bool, headers, kind, body string) error {
	var lines []string
	if w.appID != "" {
		lines = append(lines, basic(w.appID+":x"))
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
	return w.send(r)
}

func splitHeaders(headers string) []string {
	if headers == "" {
		return nil
	}
	return strings.Split(headers, "; ")
}

func resultsKey(side string) string {
	return map[string]string{
		"request": "hotline.schema.request.errors",
		"reply":   "hotline.schema.response.errors",
	}[side]
}

// results are the record's results on one side, each written as
// "part place rule expected".
func (w *world) results(side string) ([]string, error) {
	record, err := w.record()
	if err != nil {
		return nil, err
	}
	return resultsOf(record, side)
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
func (w *world) shopSpecChecks(list string) error {
	spec, err := os.ReadFile(filepath.Join("testdata", "checks", "shop.openapi.yaml"))
	if err != nil {
		return err
	}
	// A feature's background may have added the shop api without checks.
	w.integrations, w.apis = nil, nil
	w.addIntegration(integration("talon-shop-a", w.specAPI("api.shop.test", string(spec), "    checks: "+list+"\n")))
	return nil
}
