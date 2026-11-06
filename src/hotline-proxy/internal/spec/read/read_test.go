package read_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/spec/read"
)

func TestRead(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Read Suite")
}

// The readers are checked through each engine in the spectest package;
// these are the cases an engine of its own would need to know.

var _ = Describe("Reading a path value", func() {
	DescribeTable("takes the piece the spec numbers, unescaped",
		func(path string, segment int, rest bool, want string, found bool) {
			got, ok := read.PathValue(path, segment, rest)
			Expect(ok).To(Equal(found))
			Expect(got).To(Equal(want))
		},
		Entry("one piece", "/orders/a%20b", 2, false, "a b", true),
		Entry("the rest of the path", "/files/a/b/", 2, true, "a/b", true),
		Entry("no such piece", "/orders", 2, false, "", false),
		Entry("a value the path has none of", "/orders/1", -1, false, "", false),
	)
})

var _ = Describe("Reading XML", func() {
	It("says which root element the schema names", func() {
		_, err := (&read.XMLBody{Root: "Order"}).Read([]byte("<Cart/>"))
		Expect(err).To(MatchError("the root element is not Order"))
	})

	text := &read.XMLShape{Kind: read.Text}
	order := &read.XMLShape{Kind: read.Object, Props: []read.XMLProp{
		{Key: "id", Name: "id", Attribute: true, Shape: text},
		{Key: "count", Name: "count", Shape: text},
		{Key: "total", Name: "Sum", Shape: text},
		{Key: "lines", Name: "lines", Shape: &read.XMLShape{Kind: read.Array, Wrapped: true, ItemName: "line", Item: text}},
		{Key: "tags", Name: "tags", Shape: &read.XMLShape{Kind: read.Array, ItemName: "tag", Item: text}},
		{Key: "notes", Name: "note", Shape: &read.XMLShape{Kind: read.Array, Item: text}},
		{Key: "box", Name: "box", Shape: &read.XMLShape{Kind: read.Any}},
	}}
	readXML := func(shape *read.XMLShape, body string) (any, error) {
		return (&read.XMLBody{Shape: shape}).Read([]byte(body))
	}

	DescribeTable("reads an element by its shape into the data a JSON body would give",
		func(body string, want map[string]any) {
			got, err := readXML(order, body)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("an attribute, and a child read by another name",
			`<Order id="7" xmlns="urn:x" xmlns:p="urn:p"><Sum>9.5</Sum></Order>`,
			map[string]any{"id": "7", "total": "9.5"}),
		Entry("the first of an element given twice",
			`<Order><count>1</count><count>2</count></Order>`,
			map[string]any{"count": "1"}),
		Entry("a wrapped list, by its items' name",
			`<Order><lines><line>a</line><other>x</other><line>b</line></lines></Order>`,
			map[string]any{"lines": []any{"a", "b"}}),
		Entry("an unwrapped list, by its items' name",
			`<Order><tag>a</tag><count>1</count><tag>b</tag></Order>`,
			map[string]any{"tags": []any{"a", "b"}, "count": "1"}),
		Entry("an unwrapped list with no item name, by the property's name",
			`<Order><note>a</note><note>b</note></Order>`,
			map[string]any{"notes": []any{"a", "b"}}),
		Entry("an element the schema doesn't name, as it comes",
			`<Order><extra>1</extra><more><a>1</a><a>2</a><a>3</a><b/></more></Order>`,
			map[string]any{"extra": "1", "more": map[string]any{"a": []any{"1", "2", "3"}, "b": ""}}),
		Entry("not an element that would take another property's key",
			`<Order><total>1</total><count>2</count></Order>`,
			map[string]any{"count": "2"}),
		Entry("a property of no type, as it comes",
			`<Order><box><w>1</w></box></Order>`,
			map[string]any{"box": map[string]any{"w": "1"}}),
		Entry("space between elements",
			"<Order>\n  <count>1</count>\n</Order>",
			map[string]any{"count": "1"}),
		Entry("names without their prefix",
			`<p:Order xmlns:p="urn:p"><p:count>3</p:count></p:Order>`,
			map[string]any{"count": "3"}),
	)

	It("reads text in pieces as one, and only the element's own text", func() {
		got, err := readXML(text, `<a>x<!-- note -->y<![CDATA[<z>]]><b>not mine</b></a>`)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("xy<z>"))
	})

	It("reads a list with no item name as all its elements", func() {
		got, err := readXML(&read.XMLShape{Kind: read.Array, Wrapped: true, Item: text}, `<a><b>1</b><c>2</c></a>`)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]any{"1", "2"}))
	})

	It("reads a body with no shape as no value", func() {
		got, err := readXML(nil, `<a><b>1</b></a>`)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeNil())
	})

	DescribeTable("refuses a body that is not one XML document",
		func(body string) {
			_, err := readXML(order, body)
			Expect(err).To(MatchError(read.ErrNotXML))
		},
		Entry("empty", ``),
		Entry("not closed", `<Order><count>1</count>`),
		Entry("closed by another name", `<Order></Cart>`),
		Entry("closed twice", `<Order/></Order>`),
		Entry("closed in the wrong order", `<Order><count></Order></count>`),
		Entry("two roots", `<Order/><Order/>`),
		Entry("text outside the root", `<Order/>x`),
		Entry("a DOCTYPE", `<!DOCTYPE Order [<!ENTITY a "b">]><Order/>`),
		Entry("a DOCTYPE inside the root", `<Order><!DOCTYPE x></Order>`),
		Entry("not XML", `{"count": 1}`),
		Entry("broken inside", `<Order><</Order>`),
		Entry("ending inside a child", `<Order><count>1`),
		Entry("ending inside a list", `<Order><lines>`),
		Entry("ending inside a list's item", `<Order><lines><line>a`),
	)

	DescribeTable("refuses a body that ends inside an element of any shape",
		func(shape *read.XMLShape, body string) {
			_, err := readXML(shape, body)
			Expect(err).To(MatchError(read.ErrNotXML))
		},
		Entry("of no shape", nil, `<a><b>`),
		Entry("of text", text, `<a>x`),
		Entry("of text, inside a child", text, `<a>x<b>`),
		Entry("of no type", &read.XMLShape{Kind: read.Any}, `<a>x`),
		Entry("of no type, inside a child", &read.XMLShape{Kind: read.Any}, `<a><b>1`),
	)

	It("takes space around the root", func() {
		got, err := readXML(order, "<?xml version=\"1.0\"?>\n<Order><count>1</count></Order>\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]any{"count": "1"}))
	})
})

var _ = Describe("Shaping parameter values", func() {
	It("takes the first value when the schema wants no list", func() {
		Expect(read.Shape([]string{"a", "b"}, false, false)).To(Equal("a"))
	})

	It("splits a list written with commas", func() {
		Expect(read.Shape([]string{"1,2", "3"}, true, true)).To(Equal([]any{"1", "2", "3"}))
	})

	It("reads a deep object from the query", func() {
		query, _ := url.ParseQuery("filter[status]=open&filter[tags][]=a&other=1")
		Expect(read.DeepObject(query, "filter")).To(Equal(map[string]any{"status": "open", "tags": []any{"a"}}))
		Expect(read.DeepObject(query, "none")).To(BeNil())
	})
})

var _ = Describe("Turning a copied value into a record's value", func() {
	DescribeTable("keeps a value of a marked type",
		func(v any, types []string, want any, ok bool) {
			got, kept := read.Plain(v, types)
			Expect(kept).To(Equal(ok))
			if want == nil {
				Expect(got).To(BeNil())
				return
			}
			Expect(got).To(Equal(want))
		},
		Entry("text", "a", []string{"string"}, "a", true),
		Entry("a whole number", json.Number("7"), []string{"integer"}, int64(7), true),
		Entry("a number with a fraction", json.Number("1.5"), []string{"number"}, 1.5, true),
		Entry("true", true, []string{"boolean"}, true, true),
		Entry("text where a number is marked", "7", []string{"integer"}, nil, false),
		Entry("an object", map[string]any{}, []string{"string"}, nil, false),
	)

	It("cuts long text", func() {
		got, _ := read.Plain(strings.Repeat("é", 300), []string{"string"})
		Expect([]rune(got.(string))).To(HaveLen(read.LongestText))
	})
})
