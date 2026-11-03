package katydidcheck_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-spike-katydid"
)

const listSchema = `{
  "type": "object",
  "properties": {
    "Name": {"type": "string", "pattern": "^shop-"},
    "KeyCount": {"type": "integer"},
    "Ratio": {"type": "number"},
    "IsTruncated": {"type": "boolean"},
    "Kind": {"$ref": "#/definitions/Kind"},
    "Contents": {"type": "array", "items": {"$ref": "#/definitions/Object"}}
  },
  "definitions": {
    "Kind": {"type": "string", "enum": ["a", "b"]},
    "Object": {
      "type": "object", "required": ["Key", "Size"],
      "properties": {"Key": {"type": "string"}, "Size": {"type": "integer"}}
    }
  }
}`

// failsAt checks that err says the body first failed at the given place, with the given value.
// The path is the field names and list positions from the root, for example [Contents 1 Size].
func failsAt(err error, path string, value string) {
	GinkgoHelper()
	var ve *katydidcheck.ValidationError
	Expect(errors.As(err, &ve)).To(BeTrue(), "expected a ValidationError, got %v", err)
	Expect(fmt.Sprint(ve.Path)).To(Equal(path))
	Expect(ve.Value).To(Equal(value))
}

var _ = Describe("Checker", func() {
	var c *katydidcheck.Checker

	BeforeEach(func() {
		var err error
		c, err = katydidcheck.Compile([]byte(listSchema))
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("JSON", func() {
		It("accepts a valid body", func() {
			Expect(c.CheckJSON([]byte(`{"Name":"shop-x","KeyCount":2,"Contents":[{"Key":"a","Size":1}]}`))).To(BeTrue())
		})
		It("refuses a missing required field", func() {
			Expect(c.CheckJSON([]byte(`{"Contents":[{"Key":"a"}]}`))).To(BeFalse())
		})
		It("refuses a wrong pattern and a wrong enum", func() {
			Expect(c.CheckJSON([]byte(`{"Name":"other"}`))).To(BeFalse())
			Expect(c.CheckJSON([]byte(`{"Kind":"z"}`))).To(BeFalse())
		})
	})

	Describe("JSON, where the first failure is", func() {
		It("is nil for a valid body", func() {
			Expect(c.WhyJSON([]byte(`{"Name":"shop-x","Contents":[{"Key":"a","Size":1}]}`))).To(Succeed())
		})
		It("names the item that lacks a required field", func() {
			failsAt(c.WhyJSON([]byte(`{"Contents":[{"Key":"a","Size":1},{"Key":"b"}]}`)), "[Contents 1]", "")
		})
		It("names the field with a wrong pattern, enum or type", func() {
			failsAt(c.WhyJSON([]byte(`{"Name":"other"}`)), "[Name]", "other")
			failsAt(c.WhyJSON([]byte(`{"Kind":"z"}`)), "[Kind]", "z")
			failsAt(c.WhyJSON([]byte(`{"Contents":[{"Key":"a","Size":"x"}]}`)), "[Contents 0 Size]", "x")
		})
		It("reports only the first failure", func() {
			failsAt(c.WhyJSON([]byte(`{"Name":"other","Kind":"z"}`)), "[Name]", "other")
		})
		It("is a plain error for a body that is not JSON", func() {
			err := c.WhyJSON([]byte(`{"Name":`))
			Expect(err).To(HaveOccurred())
			var ve *katydidcheck.ValidationError
			Expect(errors.As(err, &ve)).To(BeFalse())
		})
	})

	Describe("XML, where the first failure is", func() {
		It("is nil for a valid body", func() {
			Expect(c.WhyXML([]byte(`<R><Name>shop-x</Name><Contents><Key>a</Key><Size>1</Size></Contents></R>`))).To(Succeed())
		})
		It("names the second list item that lacks a field", func() {
			failsAt(c.WhyXML([]byte(`<R><Contents><Key>a</Key><Size>1</Size></Contents><Contents><Key>b</Key></Contents></R>`)), "[Contents 1]", "")
		})
		It("names the field with a wrong type, pattern or enum", func() {
			failsAt(c.WhyXML([]byte(`<R><KeyCount>abc</KeyCount></R>`)), "[KeyCount]", "abc")
			failsAt(c.WhyXML([]byte(`<R><Name>other</Name></R>`)), "[Name]", "other")
			failsAt(c.WhyXML([]byte(`<R><Kind>z</Kind></R>`)), "[Kind]", "z")
			failsAt(c.WhyXML([]byte(`<R><Contents><Key>a</Key><Size>x</Size></Contents></R>`)), "[Contents 0 Size]", "x")
		})
		It("is a plain error for a body that is not well-formed XML", func() {
			err := c.WhyXML([]byte(`<R><Name>shop-x</Name>`))
			Expect(err).To(HaveOccurred())
			var ve *katydidcheck.ValidationError
			Expect(errors.As(err, &ve)).To(BeFalse())
		})
	})

	describeXML(func() *katydidcheck.Checker { return c })

	It("gives right answers when many goroutines share one checker", func() {
		var wg sync.WaitGroup
		var mu sync.Mutex
		wrong := 0
		for range 8 {
			wg.Go(func() {
				for range 200 {
					a, _ := c.CheckJSON([]byte(`{"KeyCount":1}`))
					b, _ := c.CheckJSON([]byte(`{"KeyCount":"x"}`))
					d, _ := c.CheckXML([]byte(`<R><KeyCount>1</KeyCount></R>`))
					e, _ := c.CheckXML([]byte(`<R><KeyCount>x</KeyCount></R>`))
					if !a || b || !d || e {
						mu.Lock()
						wrong++
						mu.Unlock()
					}
				}
			})
		}
		wg.Wait()
		Expect(wrong).To(BeZero())
	})
})

func describeXML(get func() *katydidcheck.Checker) {
	check := (*katydidcheck.Checker).CheckXML
	Describe("XML, same schema", func() {
		ok := func(x string) {
			GinkgoHelper()
			got, err := check(get(), []byte(x))
			Expect(err).NotTo(HaveOccurred(), x)
			Expect(got).To(BeTrue(), x)
		}
		bad := func(x string) {
			GinkgoHelper()
			got, err := check(get(), []byte(x))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeFalse(), x)
		}

		It("accepts a valid body, ignoring the namespace word", func() {
			ok(`<R xmlns="http://s3/"><Name>shop-x</Name><KeyCount>2</KeyCount><IsTruncated>false</IsTruncated>` +
				`<Contents><Key>a</Key><Size>1</Size></Contents><Contents><Key>b</Key><Size>2</Size></Contents></R>`)
		})
		It("reads one repeated element as a list of one", func() {
			ok(`<R><Contents><Key>a</Key><Size>1</Size></Contents></R>`)
		})
		It("accepts a body with no list at all", func() {
			ok(`<R><Name>shop-x</Name></R>`)
		})
		It("keeps text exact when the schema says string", func() {
			ok(`<R><Contents><Key>007</Key><Size>1</Size></Contents></R>`)
		})
		It("refuses a missing field inside the second list item", func() {
			bad(`<R><Contents><Key>a</Key><Size>1</Size></Contents><Contents><Key>b</Key></Contents></R>`)
		})
		It("refuses wrong types, patterns and enums", func() {
			bad(`<R><KeyCount>abc</KeyCount></R>`)
			bad(`<R><IsTruncated>maybe</IsTruncated></R>`)
			bad(`<R><Name>other</Name></R>`)
			bad(`<R><Kind>z</Kind></R>`)
		})
		It("refuses a body that is not well-formed XML", func() {
			_, err := check(get(), []byte(`<R><Name>shop-x</Name>`))
			Expect(err).To(HaveOccurred())
		})
		It("refuses a DOCTYPE and never reads it", func() {
			_, err := check(get(), []byte(`<!DOCTYPE r [<!ENTITY a "b">]><R><Name>shop-x</Name></R>`))
			Expect(err).To(HaveOccurred())
		})

		Describe("odd but valid bodies", func() {
			DescribeTable("are accepted",
				func(x string) { ok(x) },
				Entry("namespace prefixes on every element", `<s3:R xmlns:s3="http://x/"><s3:Name>shop-x</s3:Name><s3:Contents><s3:Key>a</s3:Key><s3:Size>1</s3:Size></s3:Contents></s3:R>`),
				Entry("single-quoted attributes and a > inside one", `<R a='1' b="x>y" xsi:type="T" xmlns:xsi="http://x/"><Name>shop-x</Name></R>`),
				Entry("indented with newlines and carriage returns", "<R>\r\n  <Name>shop-x</Name>\r\n  <Contents>\r\n    <Key>a</Key>\r\n    <Size>1</Size>\r\n  </Contents>\r\n</R>\r\n"),
				Entry("spaces around the end tag name", `<R><Name>shop-x</Name ></R >`),
				Entry("xml line, comments and processing lines around the root", `<?xml version="1.0" encoding="UTF-8"?><!-- a --><?pi x?><R><Name>shop-x</Name></R><!-- b --><?pi y?>`+"\n"),
				Entry("comment inside text and between elements", `<R><Name>shop-<!-- x -->x</Name><!-- y --><KeyCount>1</KeyCount></R>`),
				Entry("CDATA that holds markup and brackets", `<R><Name><![CDATA[shop-]]]]><![CDATA[>x]]></Name></R>`),
				Entry("entities, numeric and hex", `<R><Name>shop-&#120;&#x78;&amp;&lt;&gt;&quot;&apos;</Name></R>`),
				Entry("unicode text", `<R><Name>shop-é世界😀</Name></R>`),
				Entry("negative and padded numbers", `<R><KeyCount> -5 </KeyCount><Contents><Key>a</Key><Size>+7</Size></Contents></R>`),
				Entry("decimal numbers in several spellings", `<R><Ratio>1.5e3</Ratio><Contents><Key>a</Key><Size>1</Size></Contents></R><!-- .5 and -0 are numbers too -->`),
				Entry("a number starting with a dot", `<R><Ratio>.5</Ratio></R>`),
				Entry("minus zero", `<R><Ratio>-0</Ratio></R>`),
				Entry("empty root", `<R></R>`),
				Entry("self-closing root", `<R/>`),
				Entry("an empty string where the schema says string", `<R><Contents><Key></Key><Size>1</Size></Contents></R>`),
				Entry("names that differ only by case are other elements", `<R><name>whatever</name><NAME>1</NAME><Name>shop-x</Name></R>`),
				Entry("text mixed in with child elements", `<R>hello<Name>shop-x</Name>tail</R>`),
				Entry("elements the schema does not name, with children", `<R><Extra><Deep><Deeper>1</Deeper></Deep></Extra><Name>shop-x</Name></R>`),
				Entry("an attribute and a child with the same name", `<R Name="shop-x"><KeyCount>1</KeyCount></R>`),
				Entry("a very long text", `<R><Name>shop-`+strings.Repeat("x", 1<<20)+`</Name></R>`),
				Entry("a list of 3000 items", `<R>`+strings.Repeat(`<Contents><Key>a</Key><Size>1</Size></Contents>`, 3000)+`</R>`),
			)

			DescribeTable("are refused as invalid, not as broken XML",
				func(x string) { bad(x) },
				Entry("a number too big for 64 bits", `<R><KeyCount>99999999999999999999</KeyCount></R>`),
				Entry("an integer written as a decimal or in hex", `<R><KeyCount>1.5</KeyCount></R>`),
				Entry("an integer written with an exponent", `<R><KeyCount>1e3</KeyCount></R>`),
				Entry("hex digits", `<R><KeyCount>0x10</KeyCount></R>`),
				Entry("NaN as a number", `<R><Ratio>NaN</Ratio></R>`),
				Entry("Inf as a number", `<R><Ratio>Inf</Ratio></R>`),
				Entry("a hex float as a number", `<R><Ratio>0x1p-2</Ratio></R>`),
				Entry("a number with an underscore", `<R><Ratio>1_0</Ratio></R>`),
				Entry("an empty integer", `<R><KeyCount></KeyCount></R>`),
				Entry("true in capitals", `<R><IsTruncated>TRUE</IsTruncated></R>`),
				Entry("true as 1", `<R><IsTruncated>1</IsTruncated></R>`),
				Entry("an empty boolean", `<R><IsTruncated/></R>`),
				Entry("a self-closing list item that lacks required fields", `<R><Contents/></R>`),
				Entry("the second of two items lacking a field", `<R><Contents><Key>a</Key><Size>1</Size></Contents><Contents/></R>`),
				Entry("a wrong value hidden after many good items", `<R>`+strings.Repeat(`<Contents><Key>a</Key><Size>1</Size></Contents>`, 500)+`<Contents><Key>a</Key><Size>x</Size></Contents></R>`),
				Entry("an enum value with different case", `<R><Kind>A</Kind></R>`),
			)

			DescribeTable("are refused as broken XML",
				func(x string) {
					_, err := check(get(), []byte(x))
					Expect(err).To(HaveOccurred())
				},
				Entry("an empty body", ``),
				Entry("only spaces", "  \n "),
				Entry("only text", `hello`),
				Entry("text before the root", `hello<R/>`),
				Entry("a lone <", `<`),
				Entry("two roots", `<R/><R/>`),
				Entry("text after the root", `<R/>tail`),
				Entry("an element after the root", `<R></R><X/>`),
				Entry("a missing end tag", `<R><Name>a</Name>`),
				Entry("a wrong end tag", `<R><Name>a</Nom></R>`),
				Entry("a missing > on the end tag", `<R><Name>a</Name></R`),
				Entry("an unclosed comment", `<R><!-- x</R>`),
				Entry("an unclosed CDATA", `<R><Name><![CDATA[x</Name></R>`),
				Entry("an unclosed start tag", `<R><Name`),
				Entry("an attribute without a value", `<R a><Name>x</Name></R>`),
				Entry("an attribute without quotes", `<R a=1><Name>x</Name></R>`),
				Entry("an unclosed attribute value", `<R a="1><Name>x</Name></R>`),
				Entry("a DOCTYPE with an entity", `<!DOCTYPE r [<!ENTITY a "b">]><R/>`),
				Entry("a DOCTYPE with no inner part", `<!DOCTYPE R><R/>`),
				Entry("a DOCTYPE inside the root", `<R><!DOCTYPE x></R>`),
			)
		})
	})
}
