package labels_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/labels"
)

var _ = Describe("Kept label sets", func() {
	sut := keptSUT{}

	It("hands back the same set for the same labels", func() {
		sut.forEmptyStore()
		first := sut.SetOf(attribute.String("hotline.ingress", "connect"))
		again := sut.SetOf(attribute.String("hotline.ingress", "connect"))
		Expect(first.Equals(&again)).To(BeTrue())
		Expect(first.Len()).To(Equal(1))
		Expect(sut.SetsKept()).To(Equal(1))
	})

	It("keeps a set for each different list of labels", func() {
		sut.forEmptyStore()
		connect := sut.SetOf(attribute.String("hotline.ingress", "connect"))
		capture := sut.SetOf(attribute.String("hotline.ingress", "capture"))
		Expect(connect.Equals(&capture)).To(BeFalse())
		Expect(sut.SetsKept()).To(Equal(2))
	})

	It("keeps a set of five labels, as the records counter has", func() {
		sut.forEmptyStore()
		sut.SetOf(
			attribute.String("hotline.integration.id", "shop"),
			attribute.String("url.template", "/orders/{id}"),
			attribute.String("http.request.method", "GET"),
			attribute.Int("http.response.status_code", 200),
			attribute.String("error.type", "timeout"),
		)
		Expect(sut.SetsKept()).To(Equal(1))
	})

	// Labels that stop coming, after a reload or from an app that sends
	// made-up ids, must not hold the room for ever. The store keeps 4096
	// sets at most.
	It("starts again empty once full, and still gives the right set", func() {
		sut.forEmptyStore()
		Expect(sut.NumberedSets(4096 + 5)).To(Equal(sut.Numbers(4096 + 5)))
		Expect(sut.SetsKept()).To(Equal(5))
	})

	// A kept set holds 5 labels at most.
	It("makes a set it does not keep for more labels than a key holds", func() {
		sut.forEmptyStore()
		set := sut.SetOfLabels(6)
		Expect(set.Len()).To(Equal(6))
		Expect(sut.SetsKept()).To(BeZero())
	})
})

// keptSUT is one store of kept label sets.
type keptSUT struct{ kept *labels.Kept }

// forEmptyStore is a store that keeps no set yet.
func (s *keptSUT) forEmptyStore() { s.kept = &labels.Kept{} }

// SetOf is the set the store gives for these labels.
func (s *keptSUT) SetOf(kvs ...attribute.KeyValue) attribute.Set { return s.kept.Set(kvs...) }

// SetOfLabels is the set the store gives for n labels named a, b, c and
// on.
func (s *keptSUT) SetOfLabels(n int) attribute.Set {
	many := make([]attribute.KeyValue, n)
	for i := range many {
		many[i] = attribute.Int(string(rune('a'+i)), i)
	}
	return s.SetOf(many...)
}

// NumberedSets asks for n sets, each of one label n holding 0, 1, 2 and
// on, and says what each set it got holds.
func (s *keptSUT) NumberedSets(n int) []int64 {
	got := make([]int64, n)
	for i := range got {
		set := s.SetOf(attribute.Int("n", i))
		v, _ := set.Value("n")
		got[i] = v.AsInt64()
	}
	return got
}

// Numbers are 0 to n-1, what NumberedSets should get.
func (*keptSUT) Numbers(n int) []int64 {
	want := make([]int64, n)
	for i := range want {
		want[i] = int64(i)
	}
	return want
}

// SetsKept is how many sets the store keeps.
func (s *keptSUT) SetsKept() int { return s.kept.Len() }
