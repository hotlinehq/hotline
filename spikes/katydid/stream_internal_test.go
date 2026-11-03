package katydidcheck

import (
	"fmt"
	"io"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kj "go.katydid.org.za/parser-go-json/json"
	"go.katydid.org.za/parser-go/parse"
)

// dump lists every piece a reader gives, as text.
func dump(p parse.Parser) []string {
	var out []string
	for {
		h, err := p.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			return append(out, "ERR "+err.Error())
		}
		if h == parse.EnterHint || h == parse.LeaveHint {
			out = append(out, fmt.Sprint(h))
			continue
		}
		k, b, _ := p.Token()
		if k == parse.TrueKind || k == parse.FalseKind || k == parse.NullKind {
			b = nil // katydid's JSON reader leaves stale bytes here; they carry no meaning
		}
		out = append(out, fmt.Sprintf("%v %v %x", h, k, b))
	}
}

const eqSchema = `{
  "type": "object",
  "properties": {
    "Name": {"type": "string"}, "KeyCount": {"type": "integer"}, "Ratio": {"type": "number"},
    "IsTruncated": {"type": "boolean"}, "Tags": {"type": "array", "items": {"type": "string"}},
    "Owner": {"$ref": "#/definitions/Owner"},
    "Contents": {"type": "array", "items": {"$ref": "#/definitions/Owner"}}
  },
  "definitions": {"Owner": {"type": "object", "properties": {"ID": {"type": "string"}, "Size": {"type": "integer"}}}}
}`

var _ = Describe("streaming reader", func() {
	var c *Checker

	BeforeEach(func() {
		var err error
		c, err = Compile([]byte(eqSchema))
		Expect(err).NotTo(HaveOccurred())
	})

	It("gives katydid exactly the pieces the tree reader gives", func() {
		for _, x := range []string{
			`<R xmlns="http://x/"><Name>a</Name><KeyCount>7</KeyCount><Ratio>1.5</Ratio><IsTruncated>true</IsTruncated></R>`,
			`<?xml version="1.0"?><R><Tags>a</Tags><Tags>b</Tags><Owner><ID>007</ID><Size>3</Size></Owner></R>`,
			`<R><Contents><ID>a</ID></Contents><Contents><ID>b</ID><Size>2</Size></Contents><Name/></R>`,
			`<R><Name></Name></R>`,
			`<R><Name>a &amp; b &lt;c&gt; &#65;</Name></R>`,
			`<R><Name><![CDATA[<x>]]></Name></R>`,
			`<R><Name>a</Name><!-- note --><KeyCount>1</KeyCount></R>`,
			`<R><KeyCount>abc</KeyCount><IsTruncated>maybe</IsTruncated></R>`,
		} {
			tree, err := toHedge([]byte(x), c.root, c.defs)
			Expect(err).NotTo(HaveOccurred(), x)
			want := dump(newFixed(tree))
			c.sp.Init([]byte(x))
			Expect(dump(c.sp)).To(Equal(want), x)
		}
	})

	It("gives katydid the same pieces as its own JSON reader for the same data", func() {
		j := kj.NewJSONSchemaParser()
		j.Init([]byte(`{"Name":"a","KeyCount":7,"Ratio":1.5,"IsTruncated":false,"Owner":{},"Tags":["a","b"],"Contents":[{"ID":"x","Size":2}]}`))
		c.sp.Init([]byte(`<R><Name>a</Name><KeyCount>7</KeyCount><Ratio>1.5</Ratio><IsTruncated>false</IsTruncated><Owner/>` +
			`<Tags>a</Tags><Tags>b</Tags><Contents><ID>x</ID><Size>2</Size></Contents></R>`))
		Expect(dump(c.sp)).To(Equal(dump(j)))
	})

	It("reads attributes as fields", func() {
		ok, err := c.CheckXMLStream([]byte(`<R><Owner Size="5" ID="x"/></R>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, _ = c.CheckXMLStream([]byte(`<R><Owner Size="five"/></R>`))
		Expect(ok).To(BeFalse())
	})

	It("keeps exact text through entities and CDATA", func() {
		c2, _ := Compile([]byte(`{"type":"object","properties":{"Name":{"type":"string","pattern":"^a & b <c>$"}}}`))
		ok, err := c2.CheckXMLStream([]byte(`<R><Name>a &amp; b <![CDATA[<c>]]></Name></R>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("reports a repeated element that is not next to its siblings", func() {
		_, err := c.CheckXMLStream([]byte(`<R><Tags>a</Tags><Name>n</Name><Tags>b</Tags></R>`))
		Expect(err).To(MatchError(errNotNear))
	})

	It("reports elements where the schema expects text", func() {
		_, err := c.CheckXMLStream([]byte(`<R><Name><x/></Name></R>`))
		Expect(err).To(HaveOccurred())
	})

	It("refuses a mismatched end tag and text after the root", func() {
		_, err := c.CheckXMLStream([]byte(`<R><Name>a</Nom></R>`))
		Expect(err).To(HaveOccurred())
		_, err = c.CheckXMLStream([]byte(`<R/><R/>`))
		Expect(err).To(HaveOccurred())
	})

	It("can be reused for the next body after an error", func() {
		_, err := c.CheckXMLStream([]byte(`<R><Name>`))
		Expect(err).To(HaveOccurred())
		ok, err := c.CheckXMLStream([]byte(`<R><Name>a</Name></R>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("reads a body with no schema information for an element, by what it holds", func() {
		c3, _ := Compile([]byte(`{"type":"object"}`))
		ok, err := c3.CheckXMLStream([]byte(`<R><Free><A>1</A></Free><Text>x</Text></R>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})
})
