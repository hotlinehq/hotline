package katydidcheck_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-proxy/internal/katydidcheck"
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

var _ = Describe("Failure explained with the place, the rule and what was expected", func() {
	var c *katydidcheck.Checker

	BeforeEach(func() {
		var err error
		c, err = katydidcheck.Compile([]byte(rulesSchema))
		Expect(err).NotTo(HaveOccurred())
	})

	// says checks the place, the rule and what the schema expected. The places and rules are the ones the proxy
	// reported when the proxy first used another engine on the same bodies.
	says := func(err error, place, rule, expected string) {
		GinkgoHelper()
		var f *katydidcheck.Failure
		Expect(errors.As(err, &f)).To(BeTrue(), "expected a Failure, got %v", err)
		Expect([]string{f.Place, f.Rule, f.Expected}).To(Equal([]string{place, rule, expected}))
	}

	DescribeTable("JSON",
		func(body, place, rule, expected string) {
			says(c.WhyJSON([]byte(body)), place, rule, expected)
		},
		Entry("missing required field", `{}`, "$.Id", "required", ""),
		Entry("wrong type", `{"Id":"x","Age":"old"}`, "$.Age", "type", "integer"),
		Entry("number given as string for a string field", `{"Id":5}`, "$.Id", "type", "string"),
		Entry("minimum", `{"Id":"x","Age":0}`, "$.Age", "minimum", "1"),
		Entry("maximum", `{"Id":"x","Age":11}`, "$.Age", "maximum", "10"),
		Entry("minLength", `{"Id":"x","Tag":"a"}`, "$.Tag", "minLength", "2"),
		Entry("maxLength", `{"Id":"x","Tag":"abcde"}`, "$.Tag", "maxLength", "4"),
		Entry("enum", `{"Id":"x","Kind":"z"}`, "$.Kind", "enum", "a, b"),
		Entry("pattern", `{"Id":"x","Name":"other"}`, "$.Name", "pattern", "^shop-"),
		Entry("minItems", `{"Id":"x","Items":[]}`, "$.Items", "minItems", "1"),
		Entry("maxItems", `{"Id":"x","Items":["a","b","c"]}`, "$.Items", "maxItems", "2"),
		Entry("wrong type inside a list", `{"Id":"x","Items":["a",5]}`, "$.Items[1]", "type", "string"),
		Entry("additional property", `{"Id":"x","Inner":{"Extra":1}}`, "$.Inner.Extra", "notAllowed", ""),
		Entry("missing field in the second line", `{"Id":"x","Lines":[{"Sku":"a","Qty":1},{"Sku":"b"}]}`, "$.Lines[1].Qty", "required", ""),
		Entry("minimum inside a line", `{"Id":"x","Lines":[{"Sku":"a","Qty":0}]}`, "$.Lines[0].Qty", "minimum", "1"),
	)

	DescribeTable("XML, the same document gives the same place and rule",
		func(xml, place, rule, expected string) {
			says(c.WhyXML([]byte(xml)), place, rule, expected)
		},
		Entry("missing required field", `<R></R>`, "$.Id", "required", ""),
		Entry("wrong type", `<R><Id>x</Id><Age>old</Age></R>`, "$.Age", "type", "integer"),
		Entry("minimum", `<R><Id>x</Id><Age>0</Age></R>`, "$.Age", "minimum", "1"),
		Entry("maximum", `<R><Id>x</Id><Age>11</Age></R>`, "$.Age", "maximum", "10"),
		Entry("minLength", `<R><Id>x</Id><Tag>a</Tag></R>`, "$.Tag", "minLength", "2"),
		Entry("enum", `<R><Id>x</Id><Kind>z</Kind></R>`, "$.Kind", "enum", "a, b"),
		Entry("pattern", `<R><Id>x</Id><Name>other</Name></R>`, "$.Name", "pattern", "^shop-"),
		Entry("maxItems", `<R><Id>x</Id><Items>a</Items><Items>b</Items><Items>c</Items></R>`, "$.Items", "maxItems", "2"),
		Entry("missing field in the second line",
			`<R><Id>x</Id><Lines><Sku>a</Sku><Qty>1</Qty></Lines><Lines><Sku>b</Sku></Lines></R>`, "$.Lines[1].Qty", "required", ""),
		Entry("minimum inside a line", `<R><Id>x</Id><Lines><Sku>a</Sku><Qty>0</Qty></Lines></R>`, "$.Lines[0].Qty", "minimum", "1"),
	)

	It("keeps the path and the value next to the explanation", func() {
		err := c.WhyJSON([]byte(`{"Id":"x","Lines":[{"Sku":"a","Qty":0}]}`))
		failsAt(err, "[Lines 0 Qty]", "0")
	})
})
