// Package spectest holds the checks every JSON Schema engine of the proxy
// must pass, the same for each, so that engines give the same reports. An
// engine's own tests run them with Checks. The checks that need no
// schema engine are in the tests of the spec package.
package spectest

import (
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Ginkgo is written to be read this way, as in its tests
	. "github.com/onsi/gomega"    //nolint:staticcheck // and so is Gomega

	"hotline-proxy/internal/spec"
)

// Checks adds the shared checks, made with the engine whose constructor is
// given, to the suite of the test file that calls it:
//
//	var _ = spectest.Checks("santhosh", santhosh.NewEngine)
//
// The checks have many cases of their own: every JSON Schema rule, every
// way a parameter is written. Driving each through the whole proxy would be
// slow, so they are listed here. The behaviour as a whole is in
// features/checks.feature.
func Checks(engine string, newEngine spec.NewEngine) bool {
	return Describe("The checks made with "+engine, func() {
		parsed := func(doc string) *spec.Spec {
			s, err := spec.Parse("test.yaml", []byte(doc), newEngine)
			Expect(err).NotTo(HaveOccurred())
			return s
		}

		Describe("Checking a body against a schema", func() {
			DescribeTable("each rule is named, with what the schema expects",
				func(schema, body, want string) {
					s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: " + schema + "\nresponses: {}\n"))
					header, b := JSONBody(body)
					got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
					Expect(Written(got.Request)).To(Equal(want))
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

			It("matches a content type by its major type", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/*:\n      schema: { type: integer }\nresponses: {}\n"))
				got := s.Check(spec.Exchange{
					Method: "GET", EscapedPath: "/a",
					Header: http.Header{"Content-Type": {"application/vnd.x+json; charset=utf-8"}},
					Body:   spec.Body{Data: []byte(`"a"`)},
				})
				Expect(Written(got.Request)).To(Equal("body $ type integer"))
			})
		})

		Describe("Reading a JSON body", func() {
			check := func(schema, body string) spec.Checked {
				s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: " + schema + "\nresponses: {}\n"))
				header, b := JSONBody(body)
				return s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
			}

			It("keeps numbers as written, with every digit", func() {
				got := check(`{ maximum: 9223372036854775807 }`, `9223372036854775808`)
				Expect(Written(got.Request)).To(Equal("body $ maximum 9223372036854775807"))
			})

			It("reads null", func() {
				got := check(`{ properties: { a: { type: "null" } } }`, `{"a":null}`)
				Expect(got.Request).To(BeEmpty())
			})

			It("keeps the last of a name given twice", func() {
				got := check(`{ properties: { a: { type: string } } }`, `{"a":1,"a":"x"}`)
				Expect(got.Request).To(BeEmpty())
			})

			DescribeTable("reports a body that is not JSON",
				func(body string) {
					got := check(`{ type: object }`, body)
					Expect(Written(got.Request)).To(Equal("body $ json"))
				},
				Entry("cut short", `{"a":[1,`),
				Entry("a wrong word", `{"a":nul}`),
				Entry("a name that is not text", `{1:2}`),
				Entry("more after the value", `{} 1`),
			)

			marked := `
        type: object
        properties:
          id: { type: string, x-hotline-record: shop.id }
          count: { type: integer, x-hotline-record: shop.count }
          gift: { type: boolean, x-hotline-record: shop.gift }
          box: { type: object, properties: { weight: { type: number, x-hotline-record: shop.weight } } }`

			It("copies the marked fields as it reads the body", func() {
				got := check(marked, `{"id":"a1","count":7,"gift":true,"box":{"weight":1.5},"other":"x"}`)
				Expect(got.Request).To(BeEmpty())
				Expect(got.Copied).To(ConsistOf(
					spec.Field{Name: "shop.id", Value: "a1"},
					spec.Field{Name: "shop.count", Value: int64(7)},
					spec.Field{Name: "shop.gift", Value: true},
					spec.Field{Name: "shop.weight", Value: 1.5},
				))
			})

			It("copies only fields of the marked type", func() {
				got := check(marked, `{"id":5,"count":"seven","gift":{"a":1},"box":[1]}`)
				Expect(got.Request).NotTo(BeEmpty())
				Expect(got.Copied).To(BeEmpty())
			})

			It("copies nothing from a body that is not JSON", func() {
				got := check(marked, `{"id":"a1",`)
				Expect(Written(got.Request)).To(Equal("body $ json"))
				Expect(got.Copied).To(BeEmpty())
			})
		})

		Describe("Reading what the spec gives no schema for", func() {
			It("checks a parameter only for being there", func() {
				s := parsed(Route("parameters:\n  - { name: q, in: query, required: true, content: { application/json: {} } }\nresponses: {}\n"))
				Expect(Written(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request)).To(Equal("query q required"))
				Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "q=x"}).Request).To(BeEmpty())
			})

			DescribeTable("checks a body only for being readable",
				func(contentType, body, want string) {
					s := parsed(Route("requestBody:\n  content:\n    " + contentType + ": {}\nresponses: {}\n"))
					got := s.Check(spec.Exchange{
						Method: "GET", EscapedPath: "/a",
						Header: http.Header{"Content-Type": {contentType}}, Body: spec.Body{Data: []byte(body)},
					})
					Expect(Written(got.Request)).To(Equal(want))
				},
				Entry("JSON", "application/json", `{"anything":1}`, ""),
				Entry("JSON that is not", "application/json", `{} 1`, "body $ json"),
				Entry("XML", "application/xml", `<a><b>1</b></a>`, ""),
				Entry("XML that is not", "application/xml", `<a>`, "body $ xml"),
				Entry("a form", "application/x-www-form-urlencoded", "a=1", ""),
				Entry("a form that is not", "application/x-www-form-urlencoded", "a=%zz", "body $ form"),
			)
		})

		Describe("Reading an XML body", func() {
			It("reports a root element other than the one the schema names", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/xml:\n      schema: { type: object, xml: { name: Order } }\nresponses: {}\n"))
				got := s.Check(spec.Exchange{
					Method: "GET", EscapedPath: "/a",
					Header: http.Header{"Content-Type": {"application/xml"}}, Body: spec.Body{Data: []byte(`<Cart/>`)},
				})
				Expect(Written(got.Request)).To(Equal("body $ xml_root Order"))
			})

			It("turns text into the type the schema wants, and copies marked fields", func() {
				s := parsed(Route(`requestBody:
  content:
    application/xml:
      schema:
        type: object
        xml: { name: Order }
        properties:
          count: { type: integer, x-hotline-record: shop.count }
          gift: { type: boolean, x-hotline-record: shop.gift }
responses: {}
`))
				got := s.Check(spec.Exchange{
					Method: "GET", EscapedPath: "/a",
					Header: http.Header{"Content-Type": {"application/xml"}}, Body: spec.Body{Data: []byte(`<Order><count>7</count><gift>maybe</gift></Order>`)},
				})
				Expect(Written(got.Request)).To(Equal("body $.gift type boolean"))
				Expect(got.Copied).To(Equal([]spec.Field{{Name: "shop.count", Value: int64(7)}}))
			})
		})

		Describe("Reading form bodies and query values", func() {
			form := Route(`requestBody:
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
					Expect(Written(got.Request)).To(Equal(want))
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
				s := parsed(Route("parameters:\n  - { name: ids, in: query, explode: false, schema: { type: array, items: { type: integer } } }\nresponses: {}\n"))
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "ids=1,x"})
				Expect(Written(got.Request)).To(Equal("query ids[1] type integer"))
			})

			It("splits a list in a header with commas", func() {
				s := parsed(Route("parameters:\n  - { name: X-Ids, in: header, schema: { type: array, items: { type: integer } } }\nresponses: {}\n"))
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: http.Header{"X-Ids": {"1, 2"}}})
				Expect(Written(got.Request)).To(Equal("header X-Ids[1] type integer"))
			})

			It("leaves a value alone when its schema names no type", func() {
				s := parsed(Route("parameters:\n  - { name: q, in: query, schema: { minLength: 3 } }\nresponses: {}\n"))
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "q=12"})
				Expect(Written(got.Request)).To(Equal("query q minLength 3"))
			})
		})

		Describe("Checking the reply", func() {
			doc := Route(`responses:
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
				Expect(Written(got.Reply)).To(Equal("header X-Count type integer"))
			})
		})

		Describe("Formats", func() {
			DescribeTable("int32 and int64 hold whole numbers of their size",
				func(format, value string, fits bool) {
					s := parsed(Route("parameters:\n  - { name: n, in: query, schema: { format: " + format + " } }\nresponses: {}\n"))
					got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", RawQuery: "n=" + value})
					if fits {
						Expect(got.Request).To(BeEmpty())
					} else {
						Expect(Written(got.Request)).To(Equal("query n format " + format))
					}
				},
				Entry("the smallest int32", "int32", "-2147483648", true),
				Entry("below int32", "int32", "-2147483649", false),
				Entry("the largest int64", "int64", "9223372036854775807", true),
				Entry("text is not a number at all", "int32", "abc", true),
			)
		})

		Describe("Loading a spec", func() {
			shared := "components:\n  schemas:\n    Count: { type: integer }\n"

			It("checks a body that is only a $ref with the shared shape it names", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: { $ref: \"#/components/schemas/Count\" }\nresponses: {}\n") + shared)
				header, b := JSONBody(`"x"`)
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
				Expect(Written(got.Request)).To(Equal("body $ type integer"))
			})

			It("checks a schema that holds a $ref deeper inside", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: { allOf: [ { $ref: \"#/components/schemas/Count\" } ] }\nresponses: {}\n") + shared)
				header, b := JSONBody(`"x"`)
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
				Expect(Written(got.Request)).To(Equal("body $ type integer"))
			})

			It("keeps the rules written beside a $ref", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: { $ref: \"#/components/schemas/Count\", minimum: 5 }\nresponses: {}\n") + shared)
				header, b := JSONBody(`3`)
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
				Expect(Written(got.Request)).To(Equal("body $ minimum 5"))
			})

			It("checks a shared schema that names itself", func() {
				s := parsed(Route("requestBody:\n  content:\n    application/json:\n      schema: { $ref: \"#/components/schemas/Node\" }\nresponses: {}\n") +
					"components:\n  schemas:\n    Node: { type: object, properties: { name: { type: string }, children: { type: array, items: { $ref: \"#/components/schemas/Node\" } } } }\n")
				header, b := JSONBody(`{"name":"a","children":[{"name":"b","children":[{"name":1}]}]}`)
				got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
				Expect(Written(got.Request)).To(Equal("body $.children[0].children[0].name type string"))
			})

			It("reports each broken schema, also when the same one is written twice", func() {
				_, err := spec.Parse("test.yaml", []byte(Route("parameters:\n  - { name: q, in: query, schema: { type: integr } }\n  - { name: r, in: query, schema: { type: integr } }\nresponses: {}\n")), newEngine)
				Expect(err).To(MatchError(ContainSubstring("the schema of query q cannot be used")))
				Expect(err).To(MatchError(ContainSubstring("the schema of query r cannot be used")))
				Expect(err).NotTo(MatchError(ContainSubstring("exists")))
			})

			It("reports a broken schema of a reply header", func() {
				_, err := spec.Parse("test.yaml", []byte(Route("responses:\n  \"200\":\n    description: Fine\n    headers:\n      X-Count: { schema: { type: integr } }\n")), newEngine)
				Expect(err).To(MatchError(ContainSubstring("the schema of the 200 reply header X-Count cannot be used")))
			})

			It("refuses a broken shared schema no route uses", func() {
				_, err := spec.Parse("test.yaml", []byte(Route("responses: {}\n")+"components:\n  schemas:\n    Bad: { type: integr }\n"), newEngine)
				Expect(err).To(MatchError(ContainSubstring("the schema Bad cannot be used")))
			})
		})

		// The request is checked while the proxy waits for the vendor, and the
		// reply once it has arrived, so each side can be checked on its own
		// (Peter, 2026-10-31).
		Describe("Checking each side on its own", func() {
			var s *spec.Spec
			e := spec.Exchange{
				Method: "GET", EscapedPath: "/a", RawQuery: "n=x",
				Status: 418, ReplyHeader: http.Header{},
			}
			BeforeEach(func() {
				s = parsed(Route("parameters:\n  - { name: n, in: query, schema: { type: integer } }\nresponses:\n  \"200\": { description: ok }\n"))
			})

			It("checks only the request", func() {
				got := s.CheckRequest(e)
				Expect(Written(got.Request)).To(Equal("query n type integer"))
				Expect(got.Reply).To(BeEmpty())
			})

			It("checks only the reply", func() {
				got := s.CheckReply(e)
				Expect(got.Request).To(BeEmpty())
				Expect(Written(got.Reply)).To(Equal("status 418 not_in_spec"))
			})

			It("checks both sides together", func() {
				got := s.Check(e)
				Expect(Written(got.Request)).To(Equal("query n type integer"))
				Expect(Written(got.Reply)).To(Equal("status 418 not_in_spec"))
			})

			It("says a call with no route was skipped, on each side", func() {
				other := spec.Exchange{Method: "GET", EscapedPath: "/b", Status: 200}
				Expect(s.CheckRequest(other).Skipped).To(Equal("no_spec_route"))
				Expect(s.CheckReply(other).Skipped).To(Equal("no_spec_route"))
			})

			It("keeps the request's reason when both sides skipped part", func() {
				s = parsed(Route("requestBody:\n  content:\n    application/json: {}\nresponses:\n  \"200\":\n    description: ok\n    content:\n      application/json: {}\n"))
				got := s.Check(spec.Exchange{
					Method: "GET", EscapedPath: "/a",
					Header: http.Header{"Content-Type": {"application/json"}}, Body: spec.Body{TooLarge: true},
					Status: 200, ReplyHeader: http.Header{"Content-Type": {"text/event-stream"}},
				})
				Expect(got.Skipped).To(Equal("body_too_large"))
			})
		})
	})
}

// Route is a spec with one GET /a route, its parameters, body and replies
// written in YAML under it.
func Route(operation string) string {
	return "openapi: 3.1.0\npaths:\n  /a:\n    get:\n" + indent(operation, "      ")
}

func indent(text, by string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = by + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// Written writes results the way the feature files do: "part place rule
// expected", joined by "; ".
func Written(results []spec.Result) string {
	var all []string
	for _, r := range results {
		all = append(all, strings.Join(strings.Fields(r.Part+" "+r.Place+" "+r.Rule+" "+r.Expected), " "))
	}
	return strings.Join(all, "; ")
}

// JSONBody is a JSON body with its Content-Type header.
func JSONBody(body string) (http.Header, spec.Body) {
	return http.Header{"Content-Type": {"application/json"}}, spec.Body{Data: []byte(body)}
}
