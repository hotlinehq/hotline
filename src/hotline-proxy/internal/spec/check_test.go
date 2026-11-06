package spec_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/spectest"
)

func TestSpec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Spec Suite")
}

// These are the checks that need no JSON Schema engine: routes, required
// values, login, content types, and specs the proxy refuses. They run with
// an engine whose schemas let every value through. The checks every engine
// must pass are in the spectest package, run by each engine's own tests.

// accepting is a schema engine whose schemas let every value through.
type accepting struct{}

func acceptAll(any) (spec.SchemaCompiler, error)           { return accepting{}, nil }
func (accepting) Compile(any, string) (spec.Schema, error) { return accepting{}, nil }
func (accepting) CompileAt(string) error                   { return nil }
func (accepting) Coerce(v any) any                         { return v }
func (accepting) Types() []string                          { return nil }
func (accepting) Validate(any, string) []spec.Failure      { return nil }

var errNoStart = errors.New("the engine cannot start")

func failToStart(any) (spec.SchemaCompiler, error) { return nil, errNoStart }

// seeing is a schema engine that lets every value through and keeps the
// last value it was given to check.
type seeing struct{ seen *any }

func (e seeing) Compile(any, string) (spec.Schema, error) { return e, nil }
func (seeing) CompileAt(string) error                     { return nil }
func (seeing) Coerce(v any) any                           { return v }
func (seeing) Types() []string                            { return nil }
func (e seeing) Validate(v any, _ string) []spec.Failure {
	*e.seen = v
	return nil
}

// seeingSpec is a spec whose one route takes a JSON body, checked by a
// seeing engine that keeps the value it was given in seen.
func seeingSpec(seen *any) *spec.Spec {
	doc := spectest.Route("requestBody:\n  content:\n    application/json:\n      schema: { type: object }\nresponses: {}\n")
	s, err := spec.Parse("test.yaml", []byte(doc), func(any) (spec.SchemaCompiler, error) { return seeing{seen}, nil })
	Expect(err).NotTo(HaveOccurred())
	return s
}

// sendJSON checks a JSON request body on the route of seeingSpec.
func sendJSON(s *spec.Spec, body string) []spec.Result {
	header, b := spectest.JSONBody(body)
	return s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b}).Request
}

// decoded is the value a JSON request body reaches the schema as.
func decoded(body string) (any, []spec.Result) {
	var seen any
	results := sendJSON(seeingSpec(&seen), body)
	return seen, results
}

func parsed(doc string) *spec.Spec {
	s, err := spec.Parse("test.yaml", []byte(doc), acceptAll)
	Expect(err).NotTo(HaveOccurred())
	return s
}

var _ = Describe("Checking a body", func() {
	It("leaves out a body larger than the copy", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/x-www-form-urlencoded:\n      schema: { type: object }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			Body:   spec.Body{Data: []byte("a=1"), TooLarge: true},
		})
		Expect(got.Skipped).To(Equal("body_too_large"))
		Expect(got.Request).To(BeEmpty())
	})

	It("reports a form body that cannot be read", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/x-www-form-urlencoded:\n      schema: { type: object }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			Body:   spec.Body{Data: []byte("a=%zz")},
		})
		Expect(spectest.Written(got.Request)).To(Equal("body $ form"))
	})

	It("checks only the type when the spec gives no schema", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/json: {}\nresponses: {}\n"))
		header, b := spectest.JSONBody(`{"anything":1}`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(got.Request).To(BeEmpty())
	})

	It("reports a JSON body with more after its value", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/json:\n      schema: { type: object }\nresponses: {}\n"))
		header, b := spectest.JSONBody(`{} 1`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(spectest.Written(got.Request)).To(Equal("body $ json"))
	})

	It("reports a content type that cannot be read", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/json: {}\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {";;"}},
			Body:   spec.Body{Data: []byte(`1`)},
		})
		Expect(spectest.Written(got.Request)).To(Equal("body $ content_type application/json"))
	})
})

var _ = Describe("Reading a JSON body", func() {
	It("gives the schema every kind of JSON value", func() {
		seen, results := decoded(`{"s":"a\u00e9\n","t":true,"f":false,"z":null,"l":[1,{"in":[]}],"o":{}}`)
		Expect(results).To(BeEmpty())
		Expect(seen).To(Equal(map[string]any{
			"s": "a\u00e9\n", "t": true, "f": false, "z": nil,
			"l": []any{json.Number("1"), map[string]any{"in": []any{}}},
			"o": map[string]any{},
		}))
	})

	It("keeps numbers as written", func() {
		seen, _ := decoded(`[12345678901234567890, 1.50, -0, 2e3]`)
		Expect(seen).To(Equal([]any{json.Number("12345678901234567890"), json.Number("1.50"), json.Number("-0"), json.Number("2e3")}))
	})

	It("keeps the last of a name given twice", func() {
		seen, results := decoded(`{"a":1,"a":2}`)
		Expect(results).To(BeEmpty())
		Expect(seen).To(Equal(map[string]any{"a": json.Number("2")}))
	})

	// The body was once copied whole into a reader's buffer before it was
	// read, which took more memory than the decoded value itself.
	It("reads the body without copying it first", func() {
		var b strings.Builder
		b.WriteString(`{"Parameters":[`)
		for i := range 2000 {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"Name":"/app/settings/param-%05d","Type":"String","Value":"a value of some length, %d","Version":%d}`, i, i, i)
		}
		b.WriteString(`]}`)
		body := b.String()
		var seen any
		s := seeingSpec(&seen)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		results := sendJSON(s, body)
		runtime.ReadMemStats(&after)
		Expect(results).To(BeEmpty())
		used := float64(after.TotalAlloc-before.TotalAlloc) / float64(len(body))
		// About 6 with the body read where it is, over 9 with it copied.
		Expect(used).To(BeNumerically("<", 7.5), "memory used, as times the body's size")
	})

	DescribeTable("reports a body that is not JSON",
		func(body string) {
			_, results := decoded(body)
			Expect(spectest.Written(results)).To(Equal("body $ json"))
		},
		Entry("cut short", `{"a":[1,`),
		Entry("a wrong word", `{"a":nul}`),
		Entry("a name that is not text", `{1:2}`),
		Entry("more after the value", `{} {}`),
	)
})

var _ = Describe("Reading parameters", func() {
	It("checks a parameter with no schema only for being there", func() {
		s := parsed(spectest.Route("parameters:\n  - { name: q, in: query, required: true, content: { application/json: {} } }\nresponses: {}\n"))
		Expect(spectest.Written(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query q required"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "q=x"}).Request).To(BeEmpty())
	})

	It("ignores header parameters OpenAPI describes elsewhere", func() {
		s := parsed(spectest.Route("parameters:\n  - { name: Accept, in: header, required: true }\n  - { in: header }\nresponses: {}\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("lets a method's parameter win over the path's", func() {
		doc := "openapi: 3.1.0\npaths:\n  /a:\n    parameters:\n      - { name: q, in: query, required: true }\n    get:\n      parameters:\n        - { name: q, in: query }\n      responses: {}\n"
		Expect(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("follows a $ref to a parameter, also one inside a list", func() {
		doc := spectest.Route("parameters:\n  - { name: q, in: query, required: true }\n  - { $ref: \"#/paths/~1a/get/parameters/0\" }\nresponses: {}\n")
		Expect(spectest.Written(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query q required"))
	})
})

var _ = Describe("Logging in", func() {
	It("is open to all when one way needs nothing", func() {
		doc := spectest.Route("security: [{ key: [] }, {}]\nresponses: {}\n") +
			"components:\n  securitySchemes:\n    key: { type: apiKey, in: query, name: key }\n"
		Expect(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("is enough with any one complete way", func() {
		doc := spectest.Route("security: [{ key: [] }, { session: [] }]\nresponses: {}\n") +
			"components:\n  securitySchemes:\n    key: { type: apiKey, in: query, name: key }\n    session: { type: apiKey, in: cookie, name: sid }\n"
		s := parsed(doc)
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: http.Header{"Cookie": {"sid=1"}}}).Request).To(BeEmpty())
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "key=1"}).Request).To(BeEmpty())
		Expect(spectest.Written(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query key required"))
	})
})

var _ = Describe("Checking the reply", func() {
	doc := spectest.Route(`responses:
  2XX:
    description: Fine
    headers:
      X-Count: { schema: { type: integer } }
`)

	It("reports a reply body where the spec lists none", func() {
		got := parsed(doc).Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a", Status: 200,
			ReplyHeader: http.Header{"Content-Type": {"application/json"}},
			ReplyBody:   spec.Body{Data: []byte(`{}`)},
		})
		Expect(spectest.Written(got.Reply)).To(Equal("body $ not_in_spec"))
	})

	It("keeps the request's reason when both sides are skipped", func() {
		s := parsed(spectest.Route("requestBody:\n  content:\n    application/json: {}\nresponses:\n  \"200\":\n    content:\n      application/json: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a", Header: http.Header{"Content-Type": {"application/json"}},
			Body: spec.Body{TooLarge: true}, Status: 200,
			ReplyHeader: http.Header{"Content-Type": {"text/event-stream"}},
		})
		Expect(got.Skipped).To(Equal("body_too_large"))
	})

	It("does not check the reply when the spec lists no replies", func() {
		got := parsed("openapi: 3.1.0\npaths:\n  /a:\n    get: {}\n").Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Status: 500})
		Expect(got.Reply).To(BeEmpty())
	})
})

var _ = Describe("Specs the proxy cannot use", func() {
	DescribeTable("are refused with the problem named",
		func(doc, problem string) {
			_, err := spec.Parse("test.yaml", []byte(doc), acceptAll)
			Expect(err).To(MatchError(ContainSubstring(problem)))
		},
		Entry("a $ref to a parameter in another file",
			spectest.Route("parameters:\n  - { $ref: \"other.yaml#/p\" }\nresponses: {}\n"), "the $ref other.yaml#/p points to another file"),
		Entry("a $ref that goes round in a circle",
			spectest.Route("parameters:\n  - { $ref: \"#/components/parameters/a\" }\nresponses: {}\n")+
				"components:\n  parameters:\n    a: { $ref: \"#/components/parameters/b\" }\n    b: { $ref: \"#/components/parameters/a\" }\n",
			"a $ref goes round in a circle"),
		Entry("a value JSON cannot hold",
			spectest.Route("responses: {}\n")+"x-limit: .nan\n", "cannot be read as JSON Schema"),
	)

	It("reads replies written with plain numbers", func() {
		s := parsed(spectest.Route("responses:\n  200:\n    description: Fine\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Status: 200}).Reply).To(BeEmpty())
	})

	It("is refused when the schema engine cannot start", func() {
		_, err := spec.Parse("test.yaml", []byte(spectest.Route("responses: {}\n")), failToStart)
		Expect(err).To(MatchError(ContainSubstring("cannot be read as JSON Schema: the engine cannot start")))
	})

	It("skips a request body that is not an object", func() {
		s := parsed(spectest.Route("requestBody: just text\nresponses: {}\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})
})
