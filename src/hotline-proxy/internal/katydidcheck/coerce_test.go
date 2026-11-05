package katydidcheck_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-proxy/internal/katydidcheck"
)

var _ = Describe("Pool coerce", func() {
	const schema = `{
	  "type": "object",
	  "properties": {
	    "n": {"type": "integer"},
	    "f": {"$ref": "#/$defs/Float"},
	    "b": {"type": "boolean"},
	    "s": {"type": "string"},
	    "list": {"type": "array", "items": {"type": "integer"}},
	    "pair": {"type": "array", "prefixItems": [{"type": "boolean"}, {"type": "integer"}]},
	    "inner": {"type": "object", "properties": {"x": {"type": "number"}}, "additionalProperties": {"type": "boolean"}}
	  },
	  "$defs": {"Float": {"type": "number"}}
	}`

	It("turns text into the numbers and booleans the schema wants, and leaves the rest", func() {
		p, err := katydidcheck.NewPool([]byte(schema))
		Expect(err).NotTo(HaveOccurred())
		in := map[string]any{
			"n": "12", "f": "1.5", "b": "true", "s": "007", "list": []any{"1", "x", "3"},
			"pair": []any{"false", "2"}, "inner": map[string]any{"x": "2.5", "other": "true"}, "unknown": "9",
		}
		out, err := json.Marshal(p.Coerce(in))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(MatchJSON(`{"n":12,"f":1.5,"b":true,"s":"007","list":[1,"x",3],"pair":[false,2],"inner":{"x":2.5,"other":true},"unknown":"9"}`))
	})

	It("tells the types of the whole schema", func() {
		p, err := katydidcheck.NewPool([]byte(`{"type": ["array", "null"], "items": {"type": "string"}}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Types()).To(ConsistOf("array", "null"))
	})
})
