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
