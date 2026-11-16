package ledger_test

import (
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/ledger"
)

func TestLedger(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ledger Suite")
}

// hashOf is the content hash of one attribute, a short way to compare how
// two values are written into a hash.
func hashOf(v attribute.Value) string {
	return ledger.Text(ledger.ContentHash([]attribute.KeyValue{{Key: "x", Value: v}}))
}

var _ = Describe("Hashing a value", func() {
	// A file line is JSON, which cannot say whether a list held whole
	// numbers, decimals or true and false: verify reads every list back as
	// a list of values, so each kind of list must hash the same as that.
	DescribeTable("a typed list hashes the same as a list of values",
		func(typed attribute.Value, values []attribute.Value) {
			Expect(hashOf(typed)).To(Equal(hashOf(attribute.SliceValue(values...))))
		},
		Entry("whole numbers", attribute.Int64SliceValue([]int64{1, 2}),
			[]attribute.Value{attribute.Int64Value(1), attribute.Int64Value(2)}),
		Entry("decimals", attribute.Float64SliceValue([]float64{1.5, 2}),
			[]attribute.Value{attribute.Float64Value(1.5), attribute.Float64Value(2)}),
		Entry("true and false", attribute.BoolSliceValue([]bool{true, false}),
			[]attribute.Value{attribute.BoolValue(true), attribute.BoolValue(false)}),
	)

	It("keeps an empty value apart from an empty text", func() {
		Expect(hashOf(attribute.Value{})).NotTo(Equal(hashOf(attribute.StringValue(""))))
	})

	It("keeps a whole number apart from the same decimal", func() {
		Expect(hashOf(attribute.Int64Value(2))).NotTo(Equal(hashOf(attribute.Float64Value(2))))
	})
})

var _ = Describe("Reading a hash back", func() {
	It("reads what Text wrote", func() {
		sum := ledger.ContentHash(nil)
		Expect(ledger.Parse(ledger.Text(sum))).To(Equal(sum))
	})

	DescribeTable("refuses what is not a hash",
		func(text string) {
			_, err := ledger.Parse(text)
			Expect(err).To(MatchError(ledger.ErrNotAHash))
		},
		Entry("no sha256: in front", strings.Repeat("ab", 32)),
		Entry("too short", "sha256:abcd"),
		Entry("not hex", "sha256:"+strings.Repeat("zz", 32)),
	)
})

var _ = Describe("Checking a batch's signature", func() {
	var signed []attribute.KeyValue

	BeforeEach(func() {
		key := ledger.NewKey()
		attrs := []attribute.KeyValue{
			attribute.String("hotline.batch.root", "sha256:00"),
			attribute.String("hotline.batch.key", key.Public),
		}
		signed = append(attrs, key.Sign(attrs))
	})

	// with is the signed batch with one attribute set to another text.
	with := func(name, text string) []attribute.KeyValue {
		out := make([]attribute.KeyValue, 0, len(signed))
		for _, kv := range signed {
			if string(kv.Key) == name {
				kv = attribute.String(name, text)
			}
			out = append(out, kv)
		}
		return out
	}

	It("checks out as signed", func() {
		Expect(ledger.CheckSignature(signed)).To(BeTrue())
	})

	DescribeTable("fails",
		func(name, text string) {
			Expect(ledger.CheckSignature(with(name, text))).To(BeFalse())
		},
		Entry("with a key that does not say ed25519", "hotline.batch.key", "rsa:AAAA"),
		Entry("with a key of the wrong length", "hotline.batch.key", "ed25519:AAAA"),
		Entry("with a signature that is not base64", "hotline.batch.signature", "ed25519:!!"),
		Entry("with a changed root", "hotline.batch.root", "sha256:01"),
	)
})

var _ = Describe("Batch ids", func() {
	It("keep in order when the clock does not move", func() {
		at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
		first := ledger.NewBatchID(at, "")
		second := ledger.NewBatchID(at, first)
		third := ledger.NewBatchID(at.Add(-time.Second), second)
		Expect(second > first).To(BeTrue())
		Expect(third > second).To(BeTrue())
	})

	It("carry into the next byte when the last one is full", func() {
		at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
		previous := ledger.NewBatchID(at, "")
		full := previous[:len(previous)-2] + "ff"
		next := ledger.NewBatchID(at.Add(-time.Second), full)
		Expect(next[len(next)-2:]).To(Equal("00"))
		Expect(next > full).To(BeTrue())
	})
})
