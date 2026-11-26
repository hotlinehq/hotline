package ledger_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/vault"
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
	var signed ledger.Record

	BeforeEach(func() {
		at := time.UnixMicro(1_700_000_000_000_000)
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, at).Signer(vault.HourlyKeys{}, at)
		signed = ledger.Record{
			Attrs: []attribute.KeyValue{attribute.String("hotline.batch.root", "sha256:00")},
			At:    time.UnixMicro(1_700_000_000_000_000), Observed: time.UnixMicro(1_700_000_000_000_000),
			SeverityNumber: 9, SeverityText: "INFO", Event: "hotline.ledger.batch",
			Resource: []attribute.KeyValue{attribute.String("service.name", "hotline-proxy")},
		}
		attrs, digest := ledger.WithCertificate(ledger.BatchFormat, signed, signer)
		signatures, err := signer.Sign([][]byte{digest[:]})
		Expect(err).NotTo(HaveOccurred())
		signed.Attrs = ledger.WithSignature(attrs, signer.Algorithm, signatures[0])
	})

	// with is the signed batch with one attribute set to another text.
	with := func(name, text string) ledger.Record {
		out := signed
		out.Attrs = make([]attribute.KeyValue, 0, len(signed.Attrs))
		for _, kv := range signed.Attrs {
			if string(kv.Key) == name {
				kv = attribute.String(name, text)
			}
			out.Attrs = append(out.Attrs, kv)
		}
		return out
	}

	It("checks out as signed, with the certificate it carries", func() {
		_, err := ledger.CheckSignature(ledger.BatchFormat, signed)
		Expect(err).NotTo(HaveOccurred())
		Expect(signed.Attrs).To(ContainElement(HaveField("Key", attribute.Key("hotline.batch.certificate"))))
	})

	It("does not check out under another format word", func() {
		_, err := ledger.CheckSignature("hotline.batch.v2", signed)
		Expect(err).To(MatchError(ledger.ErrSignatureInvalid))
	})

	DescribeTable("fails",
		func(name, text string) {
			_, err := ledger.CheckSignature(ledger.BatchFormat, with(name, text))
			Expect(err).To(HaveOccurred())
		},
		Entry("with a certificate that is not base64", "hotline.batch.certificate", "!!"),
		Entry("with a certificate cut short", "hotline.batch.certificate", "AAAA"),
		Entry("with a signature that does not say ed25519", "hotline.batch.signature", "rsa-pss-sha256:AAAA"),
		Entry("with a signature that is not base64", "hotline.batch.signature", "ed25519:!!"),
		Entry("with a changed root", "hotline.batch.root", "sha256:01"),
	)

	DescribeTable("fails when a field besides the attributes changes",
		func(change func(*ledger.Record)) {
			changed := signed
			change(&changed)
			_, err := ledger.CheckSignature(ledger.BatchFormat, changed)
			Expect(err).To(MatchError(ledger.ErrSignatureInvalid))
		},
		Entry("the time", func(r *ledger.Record) { r.At = r.At.Add(time.Microsecond) }),
		Entry("the observed time", func(r *ledger.Record) { r.Observed = r.Observed.Add(time.Microsecond) }),
		Entry("the level", func(r *ledger.Record) { r.SeverityNumber = 13 }),
		Entry("the level's text", func(r *ledger.Record) { r.SeverityText = "WARN" }),
		Entry("the event", func(r *ledger.Record) { r.Event = "hotline.request" }),
		Entry("the resource", func(r *ledger.Record) { r.Resource = nil }),
	)
})

var _ = Describe("Hashing a record", func() {
	base := ledger.Record{
		Attrs: []attribute.KeyValue{attribute.Int("http.response.status_code", 200)},
		At:    time.UnixMicro(1_700_000_000_000_001), Observed: time.UnixMicro(1_700_000_000_000_002),
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Flags: 1,
		SeverityNumber: 9, SeverityText: "INFO", Event: "hotline.request",
		Resource: []attribute.KeyValue{attribute.String("service.name", "hotline-proxy")},
	}

	DescribeTable("changes when any one field changes",
		func(change func(*ledger.Record)) {
			changed := base
			change(&changed)
			Expect(ledger.RecordHash(changed)).NotTo(Equal(ledger.RecordHash(base)))
		},
		Entry("an attribute", func(r *ledger.Record) {
			r.Attrs = []attribute.KeyValue{attribute.Int("http.response.status_code", 500)}
		}),
		Entry("the time", func(r *ledger.Record) { r.At = r.At.Add(time.Microsecond) }),
		Entry("the observed time", func(r *ledger.Record) { r.Observed = r.Observed.Add(time.Microsecond) }),
		Entry("the trace id", func(r *ledger.Record) { r.TraceID = "00000000000000000000000000000001" }),
		Entry("the span id", func(r *ledger.Record) { r.SpanID = "0000000000000001" }),
		Entry("the flags", func(r *ledger.Record) { r.Flags = 0 }),
		Entry("the level", func(r *ledger.Record) { r.SeverityNumber = 17 }),
		Entry("the level's text", func(r *ledger.Record) { r.SeverityText = "ERROR" }),
		Entry("the event", func(r *ledger.Record) { r.Event = "hotline.probe" }),
		Entry("the body", func(r *ledger.Record) { r.Body = "a note" }),
		Entry("a resource label", func(r *ledger.Record) {
			r.Resource = []attribute.KeyValue{attribute.String("service.name", "another-proxy")}
		}),
	)

	It("does not depend on the order of the resource's labels", func() {
		a := base
		a.Resource = []attribute.KeyValue{attribute.String("a", "1"), attribute.String("b", "2")}
		b := base
		b.Resource = []attribute.KeyValue{attribute.String("b", "2"), attribute.String("a", "1")}
		Expect(ledger.RecordHash(a)).To(Equal(ledger.RecordHash(b)))
	})
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
	at := time.UnixMicro(1_700_000_000_123_456)
	root := ledger.SpanID{1}
	probe := []ledger.Span{{
		ID: root, Name: "probe", Kind: "client", Start: at, End: at.Add(time.Millisecond), Status: "unset", Flags: 0x101,
		Links: []ledger.Link{{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Flags: 0x101}},
	}}

	DescribeTable("changes when a span's flags or links change",
		func(change func(*ledger.Span)) {
			changed := []ledger.Span{probe[0]}
			changed[0].Links = slices.Clone(probe[0].Links)
			change(&changed[0])
			Expect(ledger.TraceHash("0102", changed)).NotTo(Equal(ledger.TraceHash("0102", probe)))
		},
		Entry("the span's flags", func(s *ledger.Span) { s.Flags = 0x301 }),
		Entry("a link's trace id", func(s *ledger.Span) { s.Links[0].TraceID = "00000000000000000000000000000001" }),
		Entry("a link's span id", func(s *ledger.Span) { s.Links[0].SpanID = "0000000000000001" }),
		Entry("a link's flags", func(s *ledger.Span) { s.Links[0].Flags = 0x100 }),
		Entry("a link taken away", func(s *ledger.Span) { s.Links = nil }),
	)

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
