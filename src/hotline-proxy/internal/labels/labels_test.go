package labels_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/labels"
)

var _ = Describe("Kept label sets", func() {
	It("hands back the same set for the same labels", func() {
		var kept labels.Kept
		first := kept.Set(attribute.String("hotline.ingress", "connect"))
		again := kept.Set(attribute.String("hotline.ingress", "connect"))
		Expect(first.Equals(&again)).To(BeTrue())
		Expect(first.Len()).To(Equal(1))
		Expect(kept.Len()).To(Equal(1))
	})

	It("keeps a set for each different list of labels", func() {
		var kept labels.Kept
		connect := kept.Set(attribute.String("hotline.ingress", "connect"))
		capture := kept.Set(attribute.String("hotline.ingress", "capture"))
		Expect(connect.Equals(&capture)).To(BeFalse())
		Expect(kept.Len()).To(Equal(2))
	})

	It("keeps a set of five labels, as the records counter has", func() {
		var kept labels.Kept
		kept.Set(
			attribute.String("hotline.integration.id", "shop"),
			attribute.String("url.template", "/orders/{id}"),
			attribute.String("http.request.method", "GET"),
			attribute.Int("http.response.status_code", 200),
			attribute.String("error.type", "timeout"),
		)
		Expect(kept.Len()).To(Equal(1))
	})

	// Labels that stop coming, after a reload or from an app that sends
	// made-up ids, must not hold the room for ever.
	It("starts again empty once full, and still gives the right set", func() {
		var kept labels.Kept
		for i := range labels.Most + 5 {
			set := kept.Set(attribute.Int("n", i))
			v, _ := set.Value("n")
			Expect(v.AsInt64()).To(BeEquivalentTo(i))
		}
		Expect(kept.Len()).To(Equal(5))
	})

	It("makes a set it does not keep for more labels than a key holds", func() {
		var kept labels.Kept
		many := make([]attribute.KeyValue, labels.Room+1)
		for i := range many {
			many[i] = attribute.Int(string(rune('a'+i)), i)
		}
		set := kept.Set(many...)
		Expect(set.Len()).To(Equal(labels.Room + 1))
		Expect(kept.Len()).To(BeZero())
	})
})
