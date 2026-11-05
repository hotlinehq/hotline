package spec_test

import (
	"errors"
	"net/http"
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
