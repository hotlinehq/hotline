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

var _ = Describe("Hashing a value", func() {
	sut := valueHashSUT{}

	// A file line is JSON, which cannot say whether a list held whole
	// numbers, decimals or true and false: verify reads every list back as
	// a list of values, so each kind of list must hash the same as that.
	DescribeTable("a typed list hashes the same as a list of values",
		func(typed attribute.Value, values []attribute.Value) {
			Expect(sut.HashOf(typed)).To(Equal(sut.HashOf(attribute.SliceValue(values...))))
		},
		Entry("whole numbers", attribute.Int64SliceValue([]int64{1, 2}),
			[]attribute.Value{attribute.Int64Value(1), attribute.Int64Value(2)}),
		Entry("decimals", attribute.Float64SliceValue([]float64{1.5, 2}),
			[]attribute.Value{attribute.Float64Value(1.5), attribute.Float64Value(2)}),
		Entry("true and false", attribute.BoolSliceValue([]bool{true, false}),
			[]attribute.Value{attribute.BoolValue(true), attribute.BoolValue(false)}),
	)

	It("keeps an empty value apart from an empty text", func() {
		Expect(sut.HashOf(attribute.Value{})).NotTo(Equal(sut.HashOf(attribute.StringValue(""))))
	})

	It("keeps a whole number apart from the same decimal", func() {
		Expect(sut.HashOf(attribute.Int64Value(2))).NotTo(Equal(sut.HashOf(attribute.Float64Value(2))))
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
	sut := batchSignatureSUT{}

	It("checks out as signed, with the certificate it carries", func() {
		sut.forSignedBatch()
		Expect(sut.Check()).To(Succeed())
		Expect(sut.AttributeNames()).To(ContainElement("hotline.batch.certificate"))
	})

	It("does not check out under another format word", func() {
		sut.forSignedBatch()
		Expect(sut.CheckAs("hotline.batch.v2")).To(MatchError(ledger.ErrSignatureInvalid))
	})

	DescribeTable("fails",
		func(name, text string) {
			sut.forSignedBatch()
			sut.SetAttribute(name, text)
			Expect(sut.Check()).NotTo(Succeed())
		},
		Entry("with a certificate that is not base64", "hotline.batch.certificate", "!!"),
		Entry("with a certificate cut short", "hotline.batch.certificate", "AAAA"),
		Entry("with a signature that does not say ed25519", "hotline.batch.signature", "rsa-pss-sha256:AAAA"),
		Entry("with a signature that is not base64", "hotline.batch.signature", "ed25519:!!"),
		Entry("with a changed root", "hotline.batch.root", "sha256:01"),
	)

	DescribeTable("fails with authorities that cannot be read",
		func(authorities attribute.KeyValue) {
			sut.forSignedBatch()
			sut.AddAttribute(authorities)
			Expect(sut.Check()).To(MatchError(vault.ErrNotACertificate))
		},
		Entry("not a list", attribute.String("hotline.batch.authorities", "AAAA")),
		Entry("a list holding what is not a certificate", attribute.Slice("hotline.batch.authorities", attribute.StringValue("AAAA"))),
	)

	DescribeTable("fails when a field besides the attributes changes",
		func(change func(*ledger.Record)) {
			sut.forSignedBatch()
			change(&sut.batch)
			Expect(sut.Check()).To(MatchError(ledger.ErrSignatureInvalid))
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
	sut := recordHashSUT{}

	DescribeTable("changes when any one field changes",
		func(change func(*ledger.Record)) {
			sut.forRecord()
			Expect(sut.HashAfter(change)).NotTo(Equal(sut.Hash()))
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
		sut.forRecord()
		ab := sut.HashWithResource(attribute.String("a", "1"), attribute.String("b", "2"))
		ba := sut.HashWithResource(attribute.String("b", "2"), attribute.String("a", "1"))
		Expect(ab).To(Equal(ba))
	})
})

var _ = Describe("Batch ids", func() {
	sut := batchIDSUT{}

	It("keep in order when the clock does not move", func() {
		sut.forClockAt(time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
		first := sut.FirstID()
		second := sut.NextID(first)
		third := sut.NextIDASecondEarlier(second)
		Expect(second > first).To(BeTrue())
		Expect(third > second).To(BeTrue())
	})

	It("carry into the next byte when the last one is full", func() {
		sut.forClockAt(time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC))
		first := sut.FirstID()
		full := first[:len(first)-2] + "ff"
		next := sut.NextIDASecondEarlier(full)
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
	sut := bodyHashSUT{}

	// The proxy can still be reading a request body when the call's record
	// takes its hash; the bytes that come later are not in the hash.
	It("ignores bytes written after the hash is taken", func() {
		sut.forRequestBody()
		sut.Write("hello")
		sum := sut.Sum()
		Expect(sut.Write(" and more")).To(Equal(9))
		Expect(sut.Sum()).To(Equal(sum))
		Expect(sut.SumOfAnotherBody("hello")).To(Equal(sum))
	})
})

var _ = Describe("Hashing a trace", func() {
	sut := traceHashSUT{}

	DescribeTable("changes when a span's flags or links change",
		func(change func(*ledger.Span)) {
			sut.forProbeTrace()
			Expect(sut.HashAfter(change)).NotTo(Equal(sut.Hash()))
		},
		Entry("the span's flags", func(s *ledger.Span) { s.Flags = 0x301 }),
		Entry("a link's trace id", func(s *ledger.Span) { s.Links[0].TraceID = "00000000000000000000000000000001" }),
		Entry("a link's span id", func(s *ledger.Span) { s.Links[0].SpanID = "0000000000000001" }),
		Entry("a link's flags", func(s *ledger.Span) { s.Links[0].Flags = 0x100 }),
		Entry("a link taken away", func(s *ledger.Span) { s.Links = nil }),
	)

	It("reads a span id as the proxy writes it, and an empty one as no span", func() {
		Expect(ledger.ParseSpanID("00f067aa0ba902b7")).To(Equal(ledger.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}))
		Expect(ledger.ParseSpanID("")).To(Equal(ledger.SpanID{}))
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
		sut.forSpansOutOfOrder()
		sut.Hash()
		Expect(sut.SpanNames()).To(Equal([]string{"later", "first"}))
	})
})

// valueHashSUT hashes one attribute value at a time.
type valueHashSUT struct{}

// HashOf is the content hash of one attribute holding v, a short way to
// compare how two values are written into a hash.
func (valueHashSUT) HashOf(v attribute.Value) string {
	return ledger.Text(ledger.ContentHash([]attribute.KeyValue{{Key: "x", Value: v}}))
}

// batchSignatureSUT is one batch record signed by a vault's key, which a
// test may change before its signature is checked.
type batchSignatureSUT struct{ batch ledger.Record }

// forSignedBatch is a batch record of one root, signed in the batch
// format.
func (s *batchSignatureSUT) forSignedBatch() {
	at := time.UnixMicro(1_700_000_000_000_000)
	signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, at).Signer(vault.HourlyKeys{}, at)
	s.batch = ledger.Record{
		Attrs: []attribute.KeyValue{attribute.String("hotline.batch.root", "sha256:00")},
		At:    at, Observed: at,
		SeverityNumber: 9, SeverityText: "INFO", Event: "hotline.ledger.batch",
		Resource: []attribute.KeyValue{attribute.String("service.name", "hotline-proxy")},
	}
	attrs, digest := ledger.WithCertificate(ledger.BatchFormat, s.batch, signer)
	signatures, err := signer.Sign([][]byte{digest[:]})
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.batch.Attrs = ledger.WithSignature(attrs, signer.Algorithm, signatures[0])
}

// SetAttribute sets the batch's attribute name to another text.
func (s *batchSignatureSUT) SetAttribute(name, text string) {
	attrs := make([]attribute.KeyValue, 0, len(s.batch.Attrs))
	for _, kv := range s.batch.Attrs {
		if string(kv.Key) == name {
			kv = attribute.String(name, text)
		}
		attrs = append(attrs, kv)
	}
	s.batch.Attrs = attrs
}

// AddAttribute adds one more attribute to the batch.
func (s *batchSignatureSUT) AddAttribute(kv attribute.KeyValue) {
	s.batch.Attrs = append(slices.Clone(s.batch.Attrs), kv)
}

// AttributeNames are the names of the batch's attributes.
func (s *batchSignatureSUT) AttributeNames() []string {
	names := make([]string, len(s.batch.Attrs))
	for i, kv := range s.batch.Attrs {
		names[i] = string(kv.Key)
	}
	return names
}

// Check checks the batch's signature in the batch format.
func (s *batchSignatureSUT) Check() error { return s.CheckAs(ledger.BatchFormat) }

// CheckAs checks the batch's signature under the format word format.
func (s *batchSignatureSUT) CheckAs(format string) error {
	_, err := ledger.CheckSignature(format, s.batch)
	return err
}

// recordHashSUT is one call's record, with every field set.
type recordHashSUT struct{ record ledger.Record }

// forRecord is a call's record with every field set.
func (s *recordHashSUT) forRecord() {
	s.record = ledger.Record{
		Attrs: []attribute.KeyValue{attribute.Int("http.response.status_code", 200)},
		At:    time.UnixMicro(1_700_000_000_000_001), Observed: time.UnixMicro(1_700_000_000_000_002),
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Flags: 1,
		SeverityNumber: 9, SeverityText: "INFO", Event: "hotline.request",
		Resource: []attribute.KeyValue{attribute.String("service.name", "hotline-proxy")},
	}
}

// Hash is the record's hash.
func (s *recordHashSUT) Hash() [32]byte { return ledger.RecordHash(s.record) }

// HashAfter is the hash of a copy of the record that change changed.
func (s *recordHashSUT) HashAfter(change func(*ledger.Record)) [32]byte {
	changed := s.record
	change(&changed)
	return ledger.RecordHash(changed)
}

// HashWithResource is the hash of a copy of the record with these
// resource labels.
func (s *recordHashSUT) HashWithResource(labels ...attribute.KeyValue) [32]byte {
	return s.HashAfter(func(r *ledger.Record) { r.Resource = labels })
}

// batchIDSUT makes batch ids by a clock that stands still.
type batchIDSUT struct{ at time.Time }

// forClockAt is a clock standing at at.
func (s *batchIDSUT) forClockAt(at time.Time) { s.at = at }

// FirstID is the id of a first batch.
func (s *batchIDSUT) FirstID() string { return ledger.NewBatchID(s.at, "") }

// NextID is the id of the batch after previous.
func (s *batchIDSUT) NextID(previous string) string { return ledger.NewBatchID(s.at, previous) }

// NextIDASecondEarlier is the id of the batch after previous, when the
// clock went back a second.
func (s *batchIDSUT) NextIDASecondEarlier(previous string) string {
	return ledger.NewBatchID(s.at.Add(-time.Second), previous)
}

// bodyHashSUT is the hash of one call's request body, while it is read.
type bodyHashSUT struct {
	hasher ledger.Hasher
	body   *ledger.BodyHash
}

// forRequestBody is the hash of a request body not read yet.
func (s *bodyHashSUT) forRequestBody() { s.body = s.newBody() }

func (s *bodyHashSUT) newBody() *ledger.BodyHash {
	return s.hasher.Body("request", [16]byte{1, 2, 3}, [8]byte{4, 5, 6})
}

// Write reads more of the body into the hash, and says how many bytes it
// took.
func (s *bodyHashSUT) Write(text string) int {
	n, err := s.body.Write([]byte(text))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return n
}

// Sum is the body's hash.
func (s *bodyHashSUT) Sum() string { return s.body.Sum() }

// SumOfAnotherBody is the hash of another body of the same call that
// holds text.
func (s *bodyHashSUT) SumOfAnotherBody(text string) string {
	other := s.newBody()
	_, err := other.Write([]byte(text))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return other.Sum()
}

// traceHashSUT is the spans of one trace.
type traceHashSUT struct{ spans []ledger.Span }

// forProbeTrace is a trace of one probe span linked to a call's span.
func (s *traceHashSUT) forProbeTrace() {
	at := time.UnixMicro(1_700_000_000_123_456)
	s.spans = []ledger.Span{{
		ID: ledger.SpanID{1}, Name: "probe", Kind: "client", Start: at, End: at.Add(time.Millisecond), Status: "unset", Flags: 0x101,
		Links: []ledger.Link{{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Flags: 0x101}},
	}}
}

// forSpansOutOfOrder is two spans, the later one first.
func (s *traceHashSUT) forSpansOutOfOrder() {
	at := time.Unix(1_700_000_000, 0)
	s.spans = []ledger.Span{
		{ID: ledger.SpanID{2}, Name: "later", Start: at.Add(time.Millisecond), End: at.Add(2 * time.Millisecond)},
		{ID: ledger.SpanID{1}, Name: "first", Start: at, End: at.Add(time.Millisecond)},
	}
}

// Hash is the trace's hash, taken by a hasher as the exporter takes it.
func (s *traceHashSUT) Hash() [32]byte {
	var h ledger.Hasher
	return h.Trace("0102", s.spans)
}

// HashAfter is the hash of a copy of the trace whose one span change
// changed.
func (s *traceHashSUT) HashAfter(change func(*ledger.Span)) [32]byte {
	changed := []ledger.Span{s.spans[0]}
	changed[0].Links = slices.Clone(s.spans[0].Links)
	change(&changed[0])
	return ledger.TraceHash("0102", changed)
}

// SpanNames are the names of the trace's spans, in their order.
func (s *traceHashSUT) SpanNames() []string {
	names := make([]string, len(s.spans))
	for i, span := range s.spans {
		names[i] = span.Name
	}
	return names
}
