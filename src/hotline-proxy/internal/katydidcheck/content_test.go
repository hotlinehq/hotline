package katydidcheck_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-proxy/internal/katydidcheck"
)

var _ = Describe("JSON inside a string", func() {
	const schema = `{
	  "type": "object",
	  "properties": {
	    "Body": {"type": "string", "contentMediaType": "application/json",
	      "contentSchema": {"type": "object", "required": ["id"], "properties": {"id": {"type": "integer"}, "lines": {"type": "array", "items": {"$ref": "#/$defs/Line"}}}}},
	    "Data": {"type": "string", "contentMediaType": "application/json", "contentEncoding": "base64",
	      "contentSchema": {"type": "object", "required": ["id"]}}
	  },
	  "$defs": {"Line": {"type": "object", "required": ["sku"], "properties": {"sku": {"type": "string"}}}}
	}`
	var p *katydidcheck.Pool

	BeforeEach(func() {
		var err error
		p, err = katydidcheck.NewPool([]byte(schema))
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts JSON that matches", func() {
		Expect(p.WhyJSON([]byte(`{"Body": "{\"id\": 1, \"lines\": [{\"sku\": \"a\"}]}"}`))).To(Succeed())
		Expect(p.WhyJSON([]byte(`{"Data": "eyJpZCI6IDF9"}`))).To(Succeed())
	})

	It("reports the place inside the JSON", func() {
		saysExactly(p.WhyJSON([]byte(`{"Body": "{\"id\": \"x\"}"}`)), "invalid at $.Body.id: type integer")
		saysExactly(p.WhyJSON([]byte(`{"Body": "{\"id\": 1, \"lines\": [{\"sku\": \"a\"}, {}]}"}`)), "invalid at $.Body.lines[1].sku: required")
		saysExactly(p.WhyJSON([]byte(`{"Body": "{}"}`)), "invalid at $.Body.id: required")
	})

	It("reports text that is not JSON and base64 that is not base64", func() {
		saysExactly(p.WhyJSON([]byte(`{"Body": "not json"}`)), "invalid at $.Body: json")
		saysExactly(p.WhyJSON([]byte(`{"Data": "***"}`)), "invalid at $.Data: base64")
		saysExactly(p.WhyJSON([]byte(`{"Data": "bm90IGpzb24="}`)), "invalid at $.Data: json")
	})

	It("reports the outer failure first", func() {
		saysExactly(p.WhyJSON([]byte(`{"Body": 5}`)), "invalid at $.Body: type string")
	})
})
