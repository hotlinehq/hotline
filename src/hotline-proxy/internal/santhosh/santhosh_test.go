package santhosh_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/spectest"
)

func TestSanthosh(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Santhosh Suite")
}

var _ = spectest.Checks("santhosh", santhosh.NewEngine)

var _ = Describe("Loading a spec with santhosh", func() {
	sut := memorySUT{}

	// The SSM spec is the largest vendor spec, with 152 calls and 1,189
	// shared shapes, so it shows how loading grows with a spec's size. It
	// took about 950 MB of short-lived memory before the checks of a
	// shared shape were reused for every $ref to it (Peter, 2026-11-01).
	It("loads the SSM spec without building a new check for every $ref", func() {
		sut.forSpecFile("aws/ssm/2014-11-06.openapi.yaml")
		Expect(sut.MegabytesToLoad()).To(BeNumerically("<", 400))
	})
})

var _ = Describe("Checking a JSON body with santhosh", func() {
	sut := memorySUT{}

	// The body was once copied whole into a json.Decoder's buffer before
	// it was read, which took more memory than the decoded value itself:
	// over 8 times the body then, about 5 now.
	It("reads the body without copying it first", func() {
		sut.forSpec(spectest.Route("requestBody:\n  content:\n    application/json:\n      schema: { type: object }\nresponses: {}\n"))
		sut.forCall(sut.ParameterListRequest(2000))
		Expect(sut.Check().Request).To(BeEmpty())
		if raceEnabled {
			// The race detector makes every allocation larger, so the
			// limit only means something without it.
			return
		}
		Expect(sut.TimesTheBody()).To(BeNumerically("<", 6.5), "memory used, as times the body's size")
	})
})

var _ = Describe("Checking an XML body with santhosh", func() {
	sut := memorySUT{}

	// An S3 listing of 50 files, as the load test sends it. Each element is
	// a few bytes of text, but it was read into a tree of its own before
	// the data santhosh checks was built from it, with a list of the
	// schema's names made again for every object, and Go's XML reader
	// looked up the namespace of every name: about 21 times the body then,
	// about 10 now (Peter, 2026-11-06).
	It("reads the body raw, without a tree of its own in between", func() {
		sut.forSpecFile("aws/s3/2006-03-01.openapi.yaml")
		sut.forCall(sut.BucketListing(50))
		Expect(sut.Check().Response).To(BeEmpty())
		got := sut.Check()
		Expect(got.Request).To(BeEmpty())
		Expect(got.Response).To(BeEmpty())
		if raceEnabled {
			// The race detector makes every allocation larger, so the
			// limit only means something without it.
			return
		}
		Expect(sut.TimesTheBody()).To(BeNumerically("<", 12), "memory used, as times the body's size")
	})
})

var _ = Describe("Checking multipart parts with santhosh", func() {
	// spec calls these only for the properties a content's schema names;
	// the engine still copes with any.
	sut := partCheckSUT{}

	It("lets through a property the schema does not name", func() {
		sut.forBodySchema(spec.Schema{"type": "object"})
		Expect(sut.JSONPart("session", `{"type":"voice"}`)).To(BeEmpty())
		Expect(sut.TextPart("model", "model", "whisper-1")).To(BeEmpty())
	})

	It("lets through any names when the content has no schema", func() {
		sut.forBodySchema(nil)
		Expect(sut.UnknownPartNames("model")).To(BeEmpty())
	})

	It("reports text parts nested too deep to read", func() {
		sut.forBodySchema(spec.Schema{"type": "object"})
		findings := sut.TextPart("filter", "filter"+strings.Repeat("[a]", 64), "x")
		Expect(spectest.Written(findings)).To(Equal("body $.filter form"))
	})
})

// memorySUT is one spec loaded by santhosh, one call to check against it,
// and the memory the last load or check took.
type memorySUT struct {
	spec      *spec.Spec
	call      spec.Exchange
	bodySize  int
	allocated uint64
}

// forSpecFile loads a vendor spec from the repository's specs folder,
// counting the memory loading takes.
func (s *memorySUT) forSpecFile(path string) {
	specs, err := os.OpenRoot("../../../../specs")
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(specs.Close)
	data, err := specs.ReadFile(path)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.allocated = allocatedBy(func() { s.spec, err = spec.Parse(filepath.Base(path), data, santhosh.NewEngine) })
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
}

// forSpec loads a spec written in the test.
func (s *memorySUT) forSpec(yaml string) {
	var err error
	s.spec, err = spec.Parse("test.yaml", []byte(yaml), santhosh.NewEngine)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
}

// forCall is the call Check checks.
func (s *memorySUT) forCall(call spec.Exchange) {
	s.call = call
	s.bodySize = len(call.Body.Data) + len(call.ResponseBody.Data)
}

// ParameterListRequest is a call that sends n SSM parameters as JSON.
func (*memorySUT) ParameterListRequest(n int) spec.Exchange {
	var b strings.Builder
	b.WriteString(`{"Parameters":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"Name":"/app/settings/param-%05d","Type":"String","Value":"a value of some length, %d","Version":%d}`, i, i, i)
	}
	b.WriteString(`]}`)
	header, data := spectest.JSONBody(b.String())
	return spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: data}
}

// BucketListing is an S3 listing of n files, as S3 answers it.
func (*memorySUT) BucketListing(n int) spec.Exchange {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Name>shop-invoices</Name><Prefix></Prefix><KeyCount>50</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
	for i := range n {
		fmt.Fprintf(&b, `<Contents><Key>invoice-%04d.pdf</Key><LastModified>2026-11-02T10:00:00.000Z</LastModified>`+
			`<Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, i, 1000+i)
	}
	b.WriteString(`</ListBucketResult>`)
	return spec.Exchange{Method: "GET", EscapedPath: "/", RawQuery: "list-type=2",
		Header: http.Header{"Authorization": {"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261102/eu-west-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=00"}},
		Status: 200, ResponseHeader: http.Header{"Content-Type": {"application/xml"}}, ResponseBody: spec.Body{Data: []byte(b.String())}}
}

// Check checks the call, counting the memory the check takes.
func (s *memorySUT) Check() spec.Checked {
	var got spec.Checked
	s.allocated = allocatedBy(func() { got = s.spec.Check(s.call) })
	return got
}

// MegabytesToLoad is the memory, in whole megabytes, that loading the spec
// took in total, short-lived memory included.
func (s *memorySUT) MegabytesToLoad() uint64 { return s.allocated / (1 << 20) }

// TimesTheBody is the memory the last check took in total, as times the
// size of the call's body.
func (s *memorySUT) TimesTheBody() float64 { return float64(s.allocated) / float64(s.bodySize) }

// allocatedBy is the bytes the process allocated while f ran, freed or
// not.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// partCheckSUT is the santhosh check of a request whose multipart body
// has one content with a schema.
type partCheckSUT struct{ check spec.RequestCheck }

// forBodySchema is a check of a request whose body content has schema.
func (s *partCheckSUT) forBodySchema(schema spec.Schema) {
	engine, err := santhosh.NewEngine(nil)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.check, err = engine.Request(spec.RequestSpec{Body: []spec.Content{{Schema: schema}}})
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
}

// JSONPart is what the check finds in a JSON part for property.
func (s *partCheckSUT) JSONPart(property, value string) []spec.Finding {
	findings, _ := s.check.JSONPart(0, property, []byte(value))
	return findings
}

// TextPart is what the check finds in one text part named name, for
// property.
func (s *partCheckSUT) TextPart(property, name, value string) []spec.Finding {
	findings, _ := s.check.TextPart(0, property, []spec.FormPart{{Name: name, Value: []byte(value)}})
	return findings
}

// UnknownPartNames is what the check says of parts with these names.
func (s *partCheckSUT) UnknownPartNames(names ...string) []spec.Finding {
	return s.check.FormPartNames(0, names)
}
