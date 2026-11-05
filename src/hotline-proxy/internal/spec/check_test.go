package spec_test

import (
	"net/http"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
)

func TestSpec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Spec Suite")
}

// The checks have many cases of their own: every JSON Schema rule, every
// way a parameter is written, every way a spec can be broken. Driving each
// through the whole proxy would be slow, so they are listed here. The
// behaviour as a whole is in features/checks.feature.

// route is a spec with one GET /a route, its parameters, body and replies
// written in YAML under it.
func route(operation string) string {
	return "openapi: 3.1.0\npaths:\n  /a:\n    get:\n" + indent(operation, "      ")
}

func indent(text, by string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = by + line
	}
	return strings.Join(lines, "\n") + "\n"
}

func parsed(doc string) *spec.Spec {
	s, err := spec.Parse("test.yaml", []byte(doc), santhosh.NewCompiler)
	Expect(err).NotTo(HaveOccurred())
	return s
}

// written writes results the way the feature files do: "part place rule
// expected", joined by "; ".
func written(results []spec.Result) string {
	var all []string
	for _, r := range results {
		all = append(all, strings.Join(strings.Fields(r.Part+" "+r.Place+" "+r.Rule+" "+r.Expected), " "))
	}
	return strings.Join(all, "; ")
}

func jsonBody(body string) (http.Header, spec.Body) {
	return http.Header{"Content-Type": {"application/json"}}, spec.Body{Data: []byte(body)}
}

var _ = Describe("Checking a body against a schema", func() {
	DescribeTable("each rule is named, with what the schema expects",
		func(schema, body, want string) {
			s := parsed(route("requestBody:\n  content:\n    application/json:\n      schema: " + schema + "\nresponses: {}\n"))
			header, b := jsonBody(body)
			got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
			Expect(written(got.Request)).To(Equal(want))
		},
		Entry("const", `{ const: 5 }`, `4`, "body $ const 5"),
		Entry("pattern", `{ type: string, pattern: "^a" }`, `"b"`, "body $ pattern ^a"),
		Entry("exclusiveMinimum", `{ exclusiveMinimum: 1 }`, `1`, "body $ exclusiveMinimum 1"),
		Entry("exclusiveMaximum", `{ exclusiveMaximum: 1 }`, `1`, "body $ exclusiveMaximum 1"),
		Entry("multipleOf", `{ multipleOf: 2 }`, `3`, "body $ multipleOf 2"),
		Entry("minLength", `{ minLength: 2 }`, `"a"`, "body $ minLength 2"),
		Entry("maxLength", `{ maxLength: 1 }`, `"ab"`, "body $ maxLength 1"),
		Entry("minItems", `{ minItems: 2 }`, `[1]`, "body $ minItems 2"),
		Entry("maxItems", `{ maxItems: 1 }`, `[1,2]`, "body $ maxItems 1"),
		Entry("minProperties", `{ minProperties: 1 }`, `{}`, "body $ minProperties 1"),
		Entry("maxProperties", `{ maxProperties: 0 }`, `{"a":1}`, "body $ maxProperties 0"),
		Entry("uniqueItems, with nothing to expect", `{ uniqueItems: true }`, `[1,1]`, "body $ uniqueItems"),
		Entry("dependentRequired", `{ dependentRequired: { a: [b] } }`, `{"a":1}`, "body $.b dependentRequired"),
		Entry("oneOf is one result, not one per choice", `{ oneOf: [{ type: string }, { type: integer }] }`, `true`, "body $ oneOf"),
		Entry("not", `{ not: { type: string } }`, `"a"`, "body $ not"),
		Entry("a schema that allows nothing", `{ properties: { a: false } }`, `{"a":1}`, "body $.a false"),
		Entry("a list item", `{ type: array, prefixItems: [{ type: string }] }`, `[1]`, "body $[0] type string"),
	)

	It("leaves out a body larger than the copy", func() {
		s := parsed(route("requestBody:\n  content:\n    application/x-www-form-urlencoded:\n      schema: { type: object }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			Body:   spec.Body{Data: []byte("a=1"), TooLarge: true},
		})
		Expect(got.Skipped).To(Equal("body_too_large"))
		Expect(got.Request).To(BeEmpty())
	})

	It("reports a form body that cannot be read", func() {
		s := parsed(route("requestBody:\n  content:\n    application/x-www-form-urlencoded:\n      schema: { type: object }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
			Body:   spec.Body{Data: []byte("a=%zz")},
		})
		Expect(written(got.Request)).To(Equal("body $ form"))
	})

	It("checks only the type when the spec gives no schema", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json: {}\nresponses: {}\n"))
		header, b := jsonBody(`{"anything":1}`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(got.Request).To(BeEmpty())
	})

	It("matches a content type by its major type", func() {
		s := parsed(route("requestBody:\n  content:\n    application/*:\n      schema: { type: integer }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {"application/vnd.x+json; charset=utf-8"}},
			Body:   spec.Body{Data: []byte(`"a"`)},
		})
		Expect(written(got.Request)).To(Equal("body $ type integer"))
	})

	It("reports a content type that cannot be read", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json: {}\nresponses: {}\n"))
		got := s.Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a",
			Header: http.Header{"Content-Type": {";;"}},
			Body:   spec.Body{Data: []byte(`1`)},
		})
		Expect(written(got.Request)).To(Equal("body $ content_type application/json"))
	})
})

var _ = Describe("Reading form bodies and query values", func() {
	form := route(`requestBody:
  content:
    application/x-www-form-urlencoded:
      schema:
        type: object
        properties:
          tags: { type: array, items: { type: integer } }
          pair: { type: array, prefixItems: [{ type: integer }, { type: boolean }] }
          price: { type: number }
          ref: { $ref: "#/components/schemas/Count" }
          mixed: { type: object, additionalProperties: { type: integer } }
responses: {}
`) + "components:\n  schemas:\n    Count: { type: integer }\n"

	DescribeTable("brackets become lists and objects, and text becomes the type the schema wants",
		func(body, want string) {
			got := parsed(form).Check(spec.Exchange{
				Method: "GET", EscapedPath: "/a",
				Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
				Body:   spec.Body{Data: []byte(body)},
			})
			Expect(written(got.Request)).To(Equal(want))
		},
		Entry("empty brackets add to a list", "tags[]=1&tags[]=x", "body $.tags[1] type integer"),
		Entry("a list from the first places", "pair[0]=1&pair[1]=maybe", "body $.pair[1] type boolean"),
		Entry("a number", "price=1.5", ""),
		Entry("a number that is not one", "price=cheap", "body $.price type number"),
		Entry("a schema behind $ref", "ref=x", "body $.ref type integer"),
		Entry("numbered and named keys stay an object", "mixed[0]=1&mixed[a]=x", "body $.mixed.a type integer"),
		Entry("a name not in the schema is left as text", "other=1", ""),
		Entry("a second value for a name is ignored", "price=1&price=x", ""),
		Entry("a form with no names", "&", ""),
	)

	It("splits a list written with commas when explode is false", func() {
		s := parsed(route("parameters:\n  - { name: ids, in: query, explode: false, schema: { type: array, items: { type: integer } } }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "ids=1,x"})
		Expect(written(got.Request)).To(Equal("query ids[1] type integer"))
	})

	It("splits a list in a header with commas", func() {
		s := parsed(route("parameters:\n  - { name: X-Ids, in: header, schema: { type: array, items: { type: integer } } }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: http.Header{"X-Ids": {"1, 2"}}})
		Expect(written(got.Request)).To(Equal("header X-Ids[1] type integer"))
	})

	It("leaves a value alone when its schema names no type", func() {
		s := parsed(route("parameters:\n  - { name: q, in: query, schema: { minLength: 3 } }\nresponses: {}\n"))
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "q=12"})
		Expect(written(got.Request)).To(Equal("query q minLength 3"))
	})

	It("checks a parameter with no schema only for being there", func() {
		s := parsed(route("parameters:\n  - { name: q, in: query, required: true, content: { application/json: {} } }\nresponses: {}\n"))
		Expect(written(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query q required"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "q=x"}).Request).To(BeEmpty())
	})

	It("ignores header parameters OpenAPI describes elsewhere", func() {
		s := parsed(route("parameters:\n  - { name: Accept, in: header, required: true }\n  - { in: header }\nresponses: {}\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("lets a method's parameter win over the path's", func() {
		doc := "openapi: 3.1.0\npaths:\n  /a:\n    parameters:\n      - { name: q, in: query, required: true }\n    get:\n      parameters:\n        - { name: q, in: query }\n      responses: {}\n"
		Expect(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("follows a $ref to a parameter, also one inside a list", func() {
		doc := route("parameters:\n  - { name: q, in: query, required: true }\n  - { $ref: \"#/paths/~1a/get/parameters/0\" }\nresponses: {}\n")
		Expect(written(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query q required"))
	})
})

var _ = Describe("Logging in", func() {
	It("is open to all when one way needs nothing", func() {
		doc := route("security: [{ key: [] }, {}]\nresponses: {}\n") +
			"components:\n  securitySchemes:\n    key: { type: apiKey, in: query, name: key }\n"
		Expect(parsed(doc).Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})

	It("is enough with any one complete way", func() {
		doc := route("security: [{ key: [] }, { session: [] }]\nresponses: {}\n") +
			"components:\n  securitySchemes:\n    key: { type: apiKey, in: query, name: key }\n    session: { type: apiKey, in: cookie, name: sid }\n"
		s := parsed(doc)
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: http.Header{"Cookie": {"sid=1"}}}).Request).To(BeEmpty())
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "key=1"}).Request).To(BeEmpty())
		Expect(written(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query key required"))
	})
})

var _ = Describe("Checking the reply", func() {
	doc := route(`responses:
  2XX:
    description: Fine
    headers:
      Content-Type: { required: true, schema: { const: x } }
      X-Count: { schema: { type: integer } }
`)

	It("matches a range of statuses and checks the headers", func() {
		got := parsed(doc).Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a", Status: 204,
			ReplyHeader: http.Header{"X-Count": {"many"}},
		})
		Expect(written(got.Reply)).To(Equal("header X-Count type integer"))
	})

	It("reports a reply body where the spec lists none", func() {
		got := parsed(doc).Check(spec.Exchange{
			Method: "GET", EscapedPath: "/a", Status: 200,
			ReplyHeader: http.Header{"Content-Type": {"application/json"}},
			ReplyBody:   spec.Body{Data: []byte(`{}`)},
		})
		Expect(written(got.Reply)).To(Equal("body $ not_in_spec"))
	})

	It("keeps the request's reason when both sides are skipped", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json: {}\nresponses:\n  \"200\":\n    content:\n      application/json: {}\n"))
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

var _ = Describe("Formats", func() {
	DescribeTable("int32 and int64 hold whole numbers of their size",
		func(format, value string, fits bool) {
			s := parsed(route("parameters:\n  - { name: n, in: query, schema: { format: " + format + " } }\nresponses: {}\n"))
			got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "n=" + value})
			if fits {
				Expect(got.Request).To(BeEmpty())
			} else {
				Expect(written(got.Request)).To(Equal("query n format " + format))
			}
		},
		Entry("the smallest int32", "int32", "-2147483648", true),
		Entry("below int32", "int32", "-2147483649", false),
		Entry("the largest int64", "int64", "9223372036854775807", true),
		Entry("text is not a number at all", "int32", "abc", true),
	)
})

var _ = Describe("Specs the proxy cannot use", func() {
	DescribeTable("are refused with the problem named",
		func(doc, problem string) {
			_, err := spec.Parse("test.yaml", []byte(doc), santhosh.NewCompiler)
			Expect(err).To(MatchError(ContainSubstring(problem)))
		},
		Entry("a $ref to a parameter in another file",
			route("parameters:\n  - { $ref: \"other.yaml#/p\" }\nresponses: {}\n"), "the $ref other.yaml#/p points to another file"),
		Entry("a $ref that goes round in a circle",
			route("parameters:\n  - { $ref: \"#/components/parameters/a\" }\nresponses: {}\n")+
				"components:\n  parameters:\n    a: { $ref: \"#/components/parameters/b\" }\n    b: { $ref: \"#/components/parameters/a\" }\n",
			"a $ref goes round in a circle"),
		Entry("a broken shared schema no route uses",
			route("responses: {}\n")+"components:\n  schemas:\n    Bad: { type: integr }\n", "the schema Bad cannot be used"),
		Entry("a value JSON cannot hold",
			route("responses: {}\n")+"x-limit: .nan\n", "cannot be read as JSON Schema"),
	)

	It("reads replies written with plain numbers", func() {
		s := parsed(route("responses:\n  200:\n    description: Fine\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Status: 200}).Reply).To(BeEmpty())
	})

	It("skips a request body that is not an object", func() {
		s := parsed(route("requestBody: just text\nresponses: {}\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})
})
