package katydidcheck_test

import (
	"errors"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	katydidcheck "hotline-proxy/internal/katydidcheck"
)

const defsSchema = `{
  "type": "object",
  "required": ["Contents"],
  "properties": {"Contents": {"type": "array", "items": {"$ref": "#/$defs/Object"}}},
  "$defs": {
    "Object": {"type": "object", "required": ["Key", "Size"],
      "properties": {"Key": {"type": "string"}, "Size": {"type": "integer", "maximum": 100}}}
  }
}`

var _ = Describe("Pool", func() {
	It("says nothing for a valid body and where the first failure is for an invalid one", func() {
		p, err := katydidcheck.NewPool([]byte(defsSchema))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.WhyJSON([]byte(`{"Contents":[{"Key":"a","Size":1}]}`))).To(Succeed())
		err = p.WhyJSON([]byte(`{"Contents":[{"Key":"a","Size":1},{"Key":"b","Size":101}]}`))
		saysExactly(err, "invalid at $.Contents[1].Size: maximum 100")
	})

	It("refuses a schema that cannot be loaded, when the pool is made", func() {
		_, err := katydidcheck.NewPool([]byte(`{"type": "object", "properties": {"a": {"$ref": "#/$defs/Missing"}}}`))
		Expect(err).To(HaveOccurred())
	})

	It("keeps a body that is not JSON apart from one that fails the schema", func() {
		p, err := katydidcheck.NewPool([]byte(defsSchema))
		Expect(err).NotTo(HaveOccurred())
		err = p.WhyJSON([]byte(`{"Contents": [`))
		Expect(err).To(HaveOccurred())
		var f *katydidcheck.Failure
		Expect(errors.As(err, &f)).To(BeFalse())
	})

	It("is used by many goroutines at the same time, each answer right", func() {
		p, err := katydidcheck.NewPool([]byte(defsSchema))
		Expect(err).NotTo(HaveOccurred())
		var wg sync.WaitGroup
		for g := 0; g < 16; g++ {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				for i := 0; i < 200; i++ {
					Expect(p.WhyJSON([]byte(`{"Contents":[{"Key":"a","Size":1}]}`))).To(Succeed())
					saysExactly(p.WhyJSON([]byte(`{"Contents":[{"Key":"a"}]}`)), "invalid at $.Contents[0].Size: required")
				}
			}()
		}
		wg.Wait()
	})
})
