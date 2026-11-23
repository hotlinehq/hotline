package santhosh_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
)

// paramsFound checks one parameter against a call's query and Cookie
// header.
func paramsFound(p spec.Param, rawQuery, cookie string) []spec.Finding {
	e, err := santhosh.NewEngine(nil)
	Expect(err).NotTo(HaveOccurred())
	check, err := e.Request(spec.RequestSpec{Params: []spec.Param{p}})
	Expect(err).NotTo(HaveOccurred())
	header := http.Header{}
	if cookie != "" {
		header.Set("Cookie", cookie)
	}
	return check.Params("/", rawQuery, header)
}

var _ = Describe("Checking the whole query string with santhosh", func() {
	It("reports nothing for a missing query string that is not required", func() {
		p := spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsText, Schema: spec.Schema{"type": "string"}}
		Expect(paramsFound(p, "", "")).To(BeEmpty())
	})

	It("only reads a query string that has no schema", func() {
		p := spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsJSON}
		Expect(paramsFound(p, "%7B%7D", "")).To(BeEmpty())
		Expect(paramsFound(p, "%7B", "")).To(Equal([]spec.Finding{{Part: "query", Place: "$", Rule: "querystring"}}))
	})

	It("reports a query string whose percent signs do not decode", func() {
		p := spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsText, Schema: spec.Schema{"type": "string"}}
		Expect(paramsFound(p, "%zz", "")).To(Equal([]spec.Finding{{Part: "query", Place: "$", Rule: "querystring"}}))
	})
})

var _ = Describe("Checking cookies with santhosh", func() {
	It("checks a form cookie whose percent signs do not decode as written", func() {
		p := spec.Param{Name: "theme", In: "cookie", Decode: true, Schema: spec.Schema{"enum": []any{"a%zz"}}}
		Expect(paramsFound(p, "", "theme=a%zz")).To(BeEmpty())
	})

	It("reports a required exploded object cookie none of whose properties is sent", func() {
		p := spec.Param{Name: "prefs", In: "cookie", Required: true, Spread: true, Explode: true,
			Schema: spec.Schema{"type": "object", "properties": map[string]any{"size": map[string]any{}}}}
		Expect(paramsFound(p, "", "session=abc")).To(Equal([]spec.Finding{{Part: "cookie", Place: "prefs", Rule: "required"}}))
	})
})
