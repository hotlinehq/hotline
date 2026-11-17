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

var _ = Describe("What a record's hash covers", func() {
	DescribeTable("each attribute of a call's line",
		func(name string, covered bool) {
			Expect(ledger.RecordCovers(name)).To(Equal(covered))
		},
		Entry("one of the record's own attributes", "hotline.integration.id", true),
		Entry("the content's own hash", "hotline.content.hash", true),
		Entry("the record's hash itself", "hotline.record.hash", false),
		Entry("the trace's spans", "hotline.trace.spans", false),
		Entry("what content: full adds", "hotline.content.request.body", false),
	)
})

var _ = Describe("Hashing a body", func() {
	ids := func() ([16]byte, [8]byte) { return [16]byte{1, 2, 3}, [8]byte{4, 5, 6} }

	// The proxy can still be reading a request body when the call's record
	// takes its hash; the bytes that come later are not in the hash.
	It("ignores bytes written after the hash is taken", func() {
		var h ledger.Hasher
		trace, span := ids()
		body := h.Body("request", trace, span)
		_, _ = body.Write([]byte("hello"))
		sum := body.Sum()
		n, err := body.Write([]byte(" and more"))
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(9))
		Expect(body.Sum()).To(Equal(sum))
		other := h.Body("request", trace, span)
		_, _ = other.Write([]byte("hello"))
		Expect(other.Sum()).To(Equal(sum))
	})
})

func spanID(text string) ledger.SpanID {
	id, err := ledger.ParseSpanID(text)
	Expect(err).NotTo(HaveOccurred())
	return id
}

var _ = Describe("Hashing a trace", func() {
	// The hash was taken when span ids were given as text; ids given as
	// bytes must make the same one, or files written before fail verify.
	It("makes the same hash as when span ids were text", func() {
		at := time.UnixMicro(1_700_000_000_123_456)
		root := spanID("00f067aa0ba902b7")
		spans := []ledger.Span{
			{ID: root, Name: "POST", Kind: "server", Start: at, End: at.Add(3 * time.Millisecond), Status: "Ok"},
			{ID: spanID("a1b2c3d4e5f60718"), Parent: root, Name: "routing", Kind: "internal", Start: at, End: at.Add(time.Millisecond), Status: "Unset",
				Attrs: []attribute.KeyValue{attribute.Bool("hotline.connection.reused", true)}},
			{ID: spanID("0a1b2c3d4e5f6071"), Parent: root, Name: "vendor call", Kind: "client", Start: at, End: at.Add(2 * time.Millisecond), Status: "Error"},
		}
		Expect(ledger.Text(ledger.TraceHash("4bf92f3577b34da6a3ce929d0e0e4736", spans))).
			To(Equal("sha256:01b2cb934226ffed4e8756705c6049959230c02fe4d3eac2523c3b6db049c3f5"))
	})

	It("reads a span id as the proxy writes it, and an empty one as no span", func() {
		Expect(spanID("00f067aa0ba902b7")).To(Equal(ledger.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}))
		Expect(spanID("")).To(Equal(ledger.SpanID{}))
	})

	// Each of these reads as an id the proxy never writes, so a file that
	// holds one was changed; reading it as a real id could hide that.
	DescribeTable("refuses a span id the proxy never writes",
		func(text string) {
			_, err := ledger.ParseSpanID(text)
			Expect(err).To(MatchError(ledger.ErrNotASpanID))
		},
		Entry("capital letters", "00F067AA0BA902B7"),
		Entry("too short", "00f067aa"),
		Entry("not hex", "00f067aa0ba902zz"),
		Entry("all zeros, which stands for no span", "0000000000000000"),
	)

	It("leaves the caller's spans in their own order", func() {
		at := time.Unix(1_700_000_000, 0)
		spans := []ledger.Span{
			{ID: ledger.SpanID{2}, Name: "later", Start: at.Add(time.Millisecond), End: at.Add(2 * time.Millisecond)},
			{ID: ledger.SpanID{1}, Name: "first", Start: at, End: at.Add(time.Millisecond)},
		}
		var h ledger.Hasher
		_ = h.Trace("0102", spans)
		Expect(spans[0].Name).To(Equal("later"))
		Expect(spans[1].Name).To(Equal("first"))
	})
})
