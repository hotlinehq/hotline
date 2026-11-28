package proxy_test

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

func bodyFieldSteps(sc *godog.ScenarioContext, sut *specChecksSUT) {
	var item string
	sc.Step(`^the record holds (\S+) = (\S+)$`, func(key, value string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return has(record, key, value)
	})
	sc.Step(`^the record holds (\S+) = "([^"]*)"$`, func(key, value string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return has(record, key, value)
	})
	sc.Step(`^the app sends POST /orders with an Idempotency-Key (\d+) characters long$`, func(n int) error {
		item = strings.Repeat("ab", n/2+1)[:n]
		return sut.app.shopCall("POST", "/orders", true, "Idempotency-Key: "+item, "json", `{"item":"A1","quantity":1}`)
	})
	sc.Step(`^the record holds (\S+) = (\S+) as a (true or false|number|whole number)$`, func(key, value, kind string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		got := record[key]
		var ok bool
		switch kind {
		case "true or false":
			_, ok = got.(bool)
		case "number":
			_, ok = got.(float64)
		default:
			_, ok = got.(int64)
		}
		if !ok {
			return failf("%s is %v, a %T, want a %s", key, got, got, kind)
		}
		return has(record, key, value)
	})
	sc.Step(`^the record has no attribute (\S+)$`, func(key string) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return hasNo(record, key)
	})
	sc.Step(`^the app sends POST /orders with "([^"]*)" and an order whose item is (\d+) characters long$`, func(headers string, n int) error {
		item = strings.Repeat("ab", n/2+1)[:n]
		return sut.app.shopCall("POST", "/orders", true, headers, "json", fmt.Sprintf(`{"item":%q,"quantity":1}`, item))
	})
	sc.Step(`^the record holds (\S+) with its first (\d+) characters$`, func(key string, n int) error {
		record, err := sut.otlp.lastRecord()
		if err != nil {
			return err
		}
		return has(record, key, item[:n])
	})
	sc.Step(`^the shop spec with x-hotline-record: (.+) on (.+)$`, func(mark, field string) error {
		sut.proxy.writeFile("specs/mark.overlay.yaml", "overlay: 1.1.0\ninfo: { title: Mark, version: \"1\" }\nactions:\n"+
			"  - target: "+field+"\n    update: { x-hotline-record: "+mark+" }\n")
		return sut.apis.shopSpec("talon-shop-a", "", "    overlays: [./specs/mark.overlay.yaml]\n")
	})
}
