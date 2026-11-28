package santhosh_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
)

var _ = Describe("Checking the whole query string with santhosh", func() {
	sut := paramCheckSUT{}

	It("reports nothing for a missing query string that is not required", func() {
		sut.forParam(spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsText, Schema: spec.Schema{"type": "string"}})
		Expect(sut.FindingsForQuery("")).To(BeEmpty())
	})

	It("only reads a query string that has no schema", func() {
		sut.forParam(spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsJSON})
		Expect(sut.FindingsForQuery("%7B%7D")).To(BeEmpty())
		Expect(sut.FindingsForQuery("%7B")).To(Equal([]spec.Finding{{Part: "query", Place: "$", Rule: "querystring"}}))
	})

	It("reports a query string whose percent signs do not decode", func() {
		sut.forParam(spec.Param{Name: "q", In: "query", WholeQuery: spec.ReadsText, Schema: spec.Schema{"type": "string"}})
		Expect(sut.FindingsForQuery("%zz")).To(Equal([]spec.Finding{{Part: "query", Place: "$", Rule: "querystring"}}))
	})
})

var _ = Describe("Checking cookies with santhosh", func() {
	sut := paramCheckSUT{}

	It("checks a form cookie whose percent signs do not decode as written", func() {
		sut.forParam(spec.Param{Name: "theme", In: "cookie", Decode: true, Schema: spec.Schema{"enum": []any{"a%zz"}}})
		Expect(sut.FindingsForCookie("theme=a%zz")).To(BeEmpty())
	})

	It("reports a required exploded object cookie none of whose properties is sent", func() {
		sut.forParam(spec.Param{Name: "prefs", In: "cookie", Required: true, Spread: true, Explode: true,
			Schema: spec.Schema{"type": "object", "properties": map[string]any{"size": map[string]any{}}}})
		Expect(sut.FindingsForCookie("session=abc")).To(Equal([]spec.Finding{{Part: "cookie", Place: "prefs", Rule: "required"}}))
	})
})

// paramCheckSUT is the santhosh check of one parameter, against a call's
// query string and Cookie header.
type paramCheckSUT struct{ check spec.RequestCheck }

// forParam is a check of a request whose only parameter is p.
func (s *paramCheckSUT) forParam(p spec.Param) {
	e, err := santhosh.NewEngine(nil)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.check, err = e.Request(spec.RequestSpec{Params: []spec.Param{p}})
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
}

// FindingsForQuery is what the check finds in a call with this query
// string and no cookies.
func (s *paramCheckSUT) FindingsForQuery(rawQuery string) []spec.Finding {
	return s.check.Params("/", rawQuery, http.Header{})
}

// FindingsForCookie is what the check finds in a call with this Cookie
// header and no query string.
func (s *paramCheckSUT) FindingsForCookie(cookie string) []spec.Finding {
	header := http.Header{}
	header.Set("Cookie", cookie)
	return s.check.Params("/", "", header)
}
