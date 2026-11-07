package spec_test

import (
	"errors"
	"fmt"
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

// These are the checks spec makes itself: routes, login, content types,
// what it hands a schema engine, and specs the proxy refuses. They run
// with a fake engine. The checks every engine must pass are in the
// spectest package, run by each engine's own tests.

// accepting is a schema engine whose checks let everything through.
type accepting struct{}

type acceptRequest struct{}

type acceptReply struct{}

func acceptAll(map[string]spec.Schema) (spec.Engine, error) { return accepting{}, nil }
func (accepting) Request(spec.RequestSpec) (spec.RequestCheck, error) {
	return acceptRequest{}, nil
}
func (accepting) Response(spec.ResponseSpec) (spec.ResponseCheck, error) {
	return acceptReply{}, nil
}
func (acceptRequest) Meta(string, string, http.Header) []spec.Result { return nil }
func (acceptRequest) Body(int, spec.Reads, []byte) ([]spec.Result, []spec.Field) {
	return nil, nil
}
func (acceptReply) Meta(http.Header) []spec.Result { return nil }
func (acceptReply) Body(int, spec.Reads, []byte) ([]spec.Result, []spec.Field) {
	return nil, nil
}

var errNoStart = errors.New("the engine cannot start")

func failToStart(map[string]spec.Schema) (spec.Engine, error) { return nil, errNoStart }

// told is a schema engine that keeps what spec tells it: the shared
// schemas, each route and reply, and each body it is asked to check.
type told struct {
	defs      map[string]spec.Schema
	requests  []spec.RequestSpec
	responses []spec.ResponseSpec
	bodies    []string
}

func (t *told) start(defs map[string]spec.Schema) (spec.Engine, error) {
	t.defs = defs
	return t, nil
}

func (t *told) Request(r spec.RequestSpec) (spec.RequestCheck, error) {
	t.requests = append(t.requests, r)
	return toldRequest{t}, nil
}

func (t *told) Response(r spec.ResponseSpec) (spec.ResponseCheck, error) {
	t.responses = append(t.responses, r)
	return toldReply{t}, nil
}

// body keeps what a told check was asked to check, and copies one field
// naming the content.
func (t *told) body(content int, reads spec.Reads, body []byte) ([]spec.Result, []spec.Field) {
	t.bodies = append(t.bodies, fmt.Sprintf("%d %d %s", content, reads, body))
	return nil, []spec.Field{{Name: "copied", Value: content}}
}

type toldRequest struct{ t *told }

func (toldRequest) Meta(string, string, http.Header) []spec.Result { return nil }
func (c toldRequest) Body(content int, reads spec.Reads, body []byte) ([]spec.Result, []spec.Field) {
	return c.t.body(content, reads, body)
}

type toldReply struct{ t *told }

func (toldReply) Meta(http.Header) []spec.Result { return nil }
func (c toldReply) Body(content int, reads spec.Reads, body []byte) ([]spec.Result, []spec.Field) {
	return c.t.body(content, reads, body)
}

// tell parses a spec with a told engine.
func tell(doc string) (*told, *spec.Spec) {
	t := &told{}
	s, err := spec.Parse("test.yaml", []byte(doc), t.start)
	Expect(err).NotTo(HaveOccurred())
	return t, s
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

var _ = Describe("What spec tells the schema engine", func() {
	It("hands over the shared schemas, each $ref named under $defs", func() {
		t, _ := tell(spectest.Route("responses: {}\n") + `components:
  schemas:
    Count: { type: integer }
    Box: { properties: { count: { $ref: "#/components/schemas/Count" }, size: { $ref: "#/components/schemas/Box/properties/count" } } }
    Anything: true
    Nothing: false
`)
		Expect(t.defs).To(Equal(map[string]spec.Schema{
			"Count": {"type": "integer"},
			"Box": {"properties": map[string]any{
				"count": map[string]any{"$ref": "#/$defs/Count"},
				"size":  map[string]any{"$ref": "#/$defs/Box/properties/count"},
			}},
			"Anything": {},
			"Nothing":  {"not": map[string]any{}},
		}))
	})

	It("describes each parameter, with where its value is", func() {
		t, _ := tell(`openapi: 3.1.0
paths:
  /a/{id}/{rest+}:
    get:
      parameters:
        - { name: id, in: path, required: true, schema: { $ref: "#/components/schemas/Id" } }
        - { name: rest, in: path, required: true }
        - { name: tags, in: query, explode: false, schema: { type: array } }
        - { name: filter, in: query, style: deepObject }
        - { name: X-Ids, in: header }
        - { name: sid, in: cookie }
      responses: {}
components:
  schemas:
    Id: { type: integer }
`)
		Expect(t.requests).To(HaveLen(1))
		Expect(t.requests[0].Params).To(Equal([]spec.Param{
			{Name: "id", In: "path", Required: true, Segment: 2, Explode: true, Schema: spec.Schema{"$ref": "#/$defs/Id"}, What: "path id"},
			{Name: "rest", In: "path", Required: true, Segment: 3, Rest: true, Explode: true, What: "path rest"},
			{Name: "tags", In: "query", Segment: -1, Schema: spec.Schema{"type": "array"}, What: "query tags"},
			{Name: "filter", In: "query", Segment: -1, DeepObject: true, What: "query filter"},
			{Name: "X-Ids", In: "header", Segment: -1, Explode: true, What: "header X-Ids"},
			{Name: "sid", In: "cookie", Segment: -1, Explode: true, What: "cookie sid"},
		}))
	})

	It("leaves out header parameters OpenAPI describes elsewhere", func() {
		t, _ := tell(spectest.Route("parameters:\n  - { name: Accept, in: header, required: true }\n  - { in: header }\nresponses: {}\n"))
		Expect(t.requests[0].Params).To(BeEmpty())
	})

	It("lets a method's parameter win over the path's", func() {
		t, _ := tell("openapi: 3.1.0\npaths:\n  /a:\n    parameters:\n      - { name: q, in: query, required: true }\n    get:\n      parameters:\n        - { name: q, in: query }\n      responses: {}\n")
		Expect(t.requests[0].Params).To(HaveLen(1))
		Expect(t.requests[0].Params[0].Required).To(BeFalse())
	})

	It("follows a $ref to a parameter, also one inside a list", func() {
		t, _ := tell(spectest.Route("parameters:\n  - { name: q, in: query, required: true }\n  - { $ref: \"#/paths/~1a/get/parameters/0\" }\nresponses: {}\n"))
		Expect(t.requests[0].Params).To(HaveLen(1))
		Expect(t.requests[0].Params[0].Required).To(BeTrue())
	})

	It("describes each content, its marked fields and its XML", func() {
		t, _ := tell(spectest.Route(`requestBody:
  content:
    application/json:
      schema:
        type: object
        properties:
          id: { type: string, x-hotline-record: shop.id }
          box: { type: object, properties: { count: { type: [integer, "null"], x-hotline-record: shop.count } } }
    application/xml:
      schema: { type: object, xml: { name: Order } }
    text/plain: {}
responses:
  "200":
    description: Fine
    headers:
      X-Count: { required: true, schema: { type: integer } }
`))
		body := t.requests[0].Body
		Expect(body).To(HaveLen(3))
		Expect(body[0].Marked).To(Equal([]spec.Marked{
			{Path: []string{"box", "count"}, Name: "shop.count", Types: []string{"integer"}},
			{Path: []string{"id"}, Name: "shop.id", Types: []string{"string"}},
		}))
		Expect(body[0].What).To(Equal("the request body application/json"))
		Expect(body[0].XML).To(BeNil())
		Expect(body[1].XML.Root).To(Equal("Order"))
		Expect(body[2].Schema).To(BeNil())
		Expect(t.responses).To(HaveLen(1))
		Expect(t.responses[0].Headers).To(Equal([]spec.Param{
			{Name: "X-Count", In: "header", Required: true, Segment: -1, Schema: spec.Schema{"type": "integer"}, What: "the 200 reply header X-Count"},
		}))
	})

	DescribeTable("asks to check a body as the content it was sent as, read the way that content is",
		func(contentType, want string) {
			t, s := tell(spectest.Route(`requestBody:
  content:
    application/json: {}
    application/xml: {}
    application/x-www-form-urlencoded: {}
    text/plain: { x-hotline-body: json }
responses: {}
`))
			got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: http.Header{"Content-Type": {contentType}}, Body: spec.Body{Data: []byte("x")}})
			Expect(t.bodies).To(Equal([]string{want}))
			Expect(got.Copied).To(HaveLen(1))
		},
		Entry("JSON", "application/json; charset=utf-8", fmt.Sprintf("0 %d x", spec.ReadsJSON)),
		Entry("XML", "application/xml", fmt.Sprintf("2 %d x", spec.ReadsXML)),
		Entry("a form", "application/x-www-form-urlencoded", fmt.Sprintf("1 %d x", spec.ReadsForm)),
		Entry("a type the spec marks as JSON", "text/plain", fmt.Sprintf("3 %d x", spec.ReadsJSON)),
	)
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
		Entry("a shared schema that is no schema",
			spectest.Route("responses: {}\n")+"components:\n  schemas:\n    Bad: 5\n", "the schema Bad cannot be used"),
		Entry("a schema that is no schema",
			spectest.Route("parameters:\n  - { name: q, in: query, schema: 5 }\nresponses: {}\n"), "the schema of query q cannot be used"),
		Entry("a value JSON cannot hold",
			spectest.Route("responses: {}\n")+"x-limit: .nan\n", "cannot be read as JSON Schema"),
	)

	It("reads replies written with plain numbers", func() {
		s := parsed(spectest.Route("responses:\n  200:\n    description: Fine\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Status: 200}).Reply).To(BeEmpty())
	})

	It("is refused when the schema engine cannot start", func() {
		_, err := spec.Parse("test.yaml", []byte(spectest.Route("responses:\n  \"200\": { description: Fine }\n")), failToStart)
		Expect(err).To(MatchError(ContainSubstring("cannot be read as JSON Schema: the engine cannot start")))
	})

	It("skips a request body that is not an object", func() {
		s := parsed(spectest.Route("requestBody: just text\nresponses: {}\n"))
		Expect(s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a"}).Request).To(BeEmpty())
	})
})

var _ = Describe("A route found once", func() {
	doc := "openapi: 3.1.0\npaths:\n" +
		"  /a/{id}:\n    get:\n      parameters:\n        - { name: id, in: path, x-hotline-redact: false }\n      responses: {}\n" +
		"  /b/{id}:\n    get:\n      parameters:\n        - { name: q, in: query }\n      responses: {}\n"

	It("is the one each check and the redaction use, without looking again", func() {
		s := parsed(doc)
		b := s.Find(spec.Exchange{Method: "GET", EscapedPath: "/b/1"})
		e := spec.Exchange{Method: "GET", EscapedPath: "/a/1", RawQuery: "q=1"}
		Expect(spectest.Written(s.CheckRequest(s.Find(e), e).Request)).To(Equal("query q not_in_spec"))
		Expect(s.CheckRequest(b, e).Request).To(BeEmpty())
		Expect(s.CheckReply(b, e).Skipped).To(BeEmpty())
		redacted := s.Redact(b, spec.Call{Origin: "https://x", EscapedPath: "/a/1"})
		Expect(redacted.Template).To(Equal("/b/{id}"))
		Expect(redacted.URLFull).To(Equal("https://x/a/REDACTED"))
	})

	It("is none for a call the spec has no route for", func() {
		s := parsed(doc)
		none := s.Find(spec.Exchange{Method: "GET", EscapedPath: "/c"})
		Expect(s.CheckRequest(none, spec.Exchange{Method: "GET", EscapedPath: "/a/1"}).Skipped).To(Equal("no_spec_route"))
		Expect(s.Redact(none, spec.Call{Origin: "https://x", EscapedPath: "/a/1"}).Template).To(BeEmpty())
	})
})
