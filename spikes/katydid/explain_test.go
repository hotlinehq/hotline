package katydidcheck_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	katydidcheck "hotline-spike-katydid"
)

const rulesSchema = `{
  "type": "object",
  "required": ["Id"],
  "properties": {
    "Id": {"type": "string"},
    "Age": {"type": "integer", "minimum": 1, "maximum": 10},
    "Tag": {"type": "string", "minLength": 2, "maxLength": 4},
    "Kind": {"type": "string", "enum": ["a", "b"]},
    "Name": {"type": "string", "pattern": "^shop-"},
    "Items": {"type": "array", "minItems": 1, "maxItems": 2, "items": {"type": "string"}},
    "Inner": {
      "type": "object", "additionalProperties": false,
      "properties": {"Ok": {"type": "boolean"}}
    },
    "Lines": {"type": "array", "items": {"$ref": "#/definitions/Line"}}
  },
  "definitions": {
    "Line": {
      "type": "object", "required": ["Sku", "Qty"],
      "properties": {"Sku": {"type": "string"}, "Qty": {"type": "integer", "minimum": 1}}
    }
  }
}`

// santhoshSays gives what santhosh reports for the body: the place, the rule and what was expected.
// It is the same text the proxy builds today.
func santhoshSays(schemaText, body string) (place, rule, expected string) {
	GinkgoHelper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemaText))
	Expect(err).NotTo(HaveOccurred())
	c := jsonschema.NewCompiler()
	Expect(c.AddResource("schema.json", doc)).To(Succeed())
	s, err := c.Compile("schema.json")
	Expect(err).NotTo(HaveOccurred())
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	var invalid *jsonschema.ValidationError
	Expect(errors.As(s.Validate(instance), &invalid)).To(BeTrue(), "santhosh should refuse "+body)
	var leaves []*jsonschema.ValidationError
	var collect func(e *jsonschema.ValidationError)
	collect = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, c := range e.Causes {
			collect(c)
		}
	}
	collect(invalid)
	Expect(leaves).To(HaveLen(1), "use a body with one failure")
	e := leaves[0]
	place = "$"
	for _, t := range e.InstanceLocation {
		if _, err := strconv.Atoi(t); err == nil {
			place += "[" + t + "]"
		} else {
			place += "." + t
		}
	}
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		return place + "." + k.Missing[0], "required", ""
	case *kind.AdditionalProperties:
		return place + "." + k.Properties[0], "additionalProperties", ""
	case *kind.Type:
		return place, "type", strings.Join(k.Want, " or ")
	case *kind.Enum:
		want := []string{}
		for _, w := range k.Want {
			want = append(want, fmt.Sprint(w))
		}
		return place, "enum", strings.Join(want, ", ")
	case *kind.Pattern:
		return place, "pattern", k.Want
	case *kind.Minimum:
		return place, "minimum", k.Want.RatString()
	case *kind.Maximum:
		return place, "maximum", k.Want.RatString()
	case *kind.MinLength:
		return place, "minLength", strconv.Itoa(k.Want)
	case *kind.MaxLength:
		return place, "maxLength", strconv.Itoa(k.Want)
	case *kind.MinItems:
		return place, "minItems", strconv.Itoa(k.Want)
	case *kind.MaxItems:
		return place, "maxItems", strconv.Itoa(k.Want)
	}
	Fail(fmt.Sprintf("unexpected error kind %T", e.ErrorKind))
	return
}

var _ = Describe("Failure explained like santhosh does", func() {
	var c *katydidcheck.Checker

	BeforeEach(func() {
		var err error
		c, err = katydidcheck.Compile([]byte(rulesSchema))
		Expect(err).NotTo(HaveOccurred())
	})

	same := func(err error, body string) {
		GinkgoHelper()
		var f *katydidcheck.Failure
		Expect(errors.As(err, &f)).To(BeTrue(), "expected a Failure, got %v", err)
		place, rule, expected := santhoshSays(rulesSchema, body)
		Expect([]string{f.Place, f.Rule, f.Expected}).To(Equal([]string{place, rule, expected}))
	}

	DescribeTable("JSON",
		func(body string) {
			same(c.WhyJSON([]byte(body)), body)
		},
		Entry("missing required field", `{}`),
		Entry("wrong type", `{"Id":"x","Age":"old"}`),
		Entry("number given as string for a string field", `{"Id":5}`),
		Entry("minimum", `{"Id":"x","Age":0}`),
		Entry("maximum", `{"Id":"x","Age":11}`),
		Entry("minLength", `{"Id":"x","Tag":"a"}`),
		Entry("maxLength", `{"Id":"x","Tag":"abcde"}`),
		Entry("enum", `{"Id":"x","Kind":"z"}`),
		Entry("pattern", `{"Id":"x","Name":"other"}`),
		Entry("minItems", `{"Id":"x","Items":[]}`),
		Entry("maxItems", `{"Id":"x","Items":["a","b","c"]}`),
		Entry("wrong type inside a list", `{"Id":"x","Items":["a",5]}`),
		Entry("additional property", `{"Id":"x","Inner":{"Extra":1}}`),
		Entry("missing field in the second line", `{"Id":"x","Lines":[{"Sku":"a","Qty":1},{"Sku":"b"}]}`),
		Entry("minimum inside a line", `{"Id":"x","Lines":[{"Sku":"a","Qty":0}]}`),
	)

	DescribeTable("XML, the same document",
		func(xml string, jsonBody string) {
			same(c.WhyXML([]byte(xml)), jsonBody)
		},
		Entry("missing required field", `<R></R>`, `{}`),
		Entry("wrong type", `<R><Id>x</Id><Age>old</Age></R>`, `{"Id":"x","Age":"old"}`),
		Entry("minimum", `<R><Id>x</Id><Age>0</Age></R>`, `{"Id":"x","Age":0}`),
		Entry("maximum", `<R><Id>x</Id><Age>11</Age></R>`, `{"Id":"x","Age":11}`),
		Entry("minLength", `<R><Id>x</Id><Tag>a</Tag></R>`, `{"Id":"x","Tag":"a"}`),
		Entry("enum", `<R><Id>x</Id><Kind>z</Kind></R>`, `{"Id":"x","Kind":"z"}`),
		Entry("pattern", `<R><Id>x</Id><Name>other</Name></R>`, `{"Id":"x","Name":"other"}`),
		Entry("maxItems", `<R><Id>x</Id><Items>a</Items><Items>b</Items><Items>c</Items></R>`, `{"Id":"x","Items":["a","b","c"]}`),
		Entry("missing field in the second line",
			`<R><Id>x</Id><Lines><Sku>a</Sku><Qty>1</Qty></Lines><Lines><Sku>b</Sku></Lines></R>`,
			`{"Id":"x","Lines":[{"Sku":"a","Qty":1},{"Sku":"b"}]}`),
		Entry("minimum inside a line", `<R><Id>x</Id><Lines><Sku>a</Sku><Qty>0</Qty></Lines></R>`, `{"Id":"x","Lines":[{"Sku":"a","Qty":0}]}`),
	)

	It("keeps the path and the value next to the explanation", func() {
		err := c.WhyJSON([]byte(`{"Id":"x","Lines":[{"Sku":"a","Qty":0}]}`))
		failsAt(err, "[Lines 0 Qty]", "0")
	})
})
