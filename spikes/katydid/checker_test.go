package katydidcheck_test

import (
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

	for name, check := range map[string]func(*katydidcheck.Checker, []byte) (bool, error){
		"tree reader":      (*katydidcheck.Checker).CheckXML,
		"streaming reader": (*katydidcheck.Checker).CheckXMLStream,
	} {
		check := check
		describeXML(name, func() *katydidcheck.Checker { return c }, check)
	}

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
					f, _ := c.CheckXMLStream([]byte(`<R><KeyCount>1</KeyCount></R>`))
					g, _ := c.CheckXMLStream([]byte(`<R><KeyCount>x</KeyCount></R>`))
					if !a || b || !d || e || !f || g {
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

func describeXML(name string, get func() *katydidcheck.Checker, check func(*katydidcheck.Checker, []byte) (bool, error)) {
	Describe("XML, same schema, "+name, func() {
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
	})
}
