package santhosh_test

import (
	"fmt"
	"net/http"
	"os"
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
	// The SSM spec is the largest vendor spec, with 152 calls and 1,189
	// shared shapes, so it shows how loading grows with a spec's size. It
	// took about 950 MB of short-lived memory before the checks of a
	// shared shape were reused for every $ref to it (Peter, 2026-11-01).
	It("loads the SSM spec without building a new check for every $ref", func() {
		data, err := os.ReadFile("../../../../specs/aws/ssm/2014-11-06.openapi.yaml")
		Expect(err).NotTo(HaveOccurred())
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = spec.Parse("ssm.yaml", data, santhosh.NewEngine)
		runtime.ReadMemStats(&after)
		Expect(err).NotTo(HaveOccurred())
		const mb = 1 << 20
		Expect((after.TotalAlloc - before.TotalAlloc) / mb).To(BeNumerically("<", 400))
	})
})

var _ = Describe("Checking a JSON body with santhosh", func() {
	// The body was once copied whole into a json.Decoder's buffer before
	// it was read, which took more memory than the decoded value itself:
	// over 8 times the body then, about 5 now.
	It("reads the body without copying it first", func() {
		var b strings.Builder
		b.WriteString(`{"Parameters":[`)
		for i := range 2000 {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"Name":"/app/settings/param-%05d","Type":"String","Value":"a value of some length, %d","Version":%d}`, i, i, i)
		}
		b.WriteString(`]}`)
		body := b.String()
		s, err := spec.Parse("test.yaml", []byte(spectest.Route("requestBody:\n  content:\n    application/json:\n      schema: { type: object }\nresponses: {}\n")), santhosh.NewEngine)
		Expect(err).NotTo(HaveOccurred())
		header, data := spectest.JSONBody(body)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: data})
		runtime.ReadMemStats(&after)
		Expect(got.Request).To(BeEmpty())
		used := float64(after.TotalAlloc-before.TotalAlloc) / float64(len(body))
		Expect(used).To(BeNumerically("<", 6.5), "memory used, as times the body's size")
	})
})

var _ = Describe("Checking an XML body with santhosh", func() {
	// An S3 listing of 50 files, as the load test sends it. Each element is
	// a few bytes of text, but it was read into a tree of its own before
	// the data santhosh checks was built from it, with a list of the
	// schema's names made again for every object, and Go's XML reader
	// looked up the namespace of every name: about 21 times the body then,
	// about 10 now (Peter, 2026-11-06).
	It("reads the body raw, without a tree of its own in between", func() {
		data, err := os.ReadFile("../../../../specs/aws/s3/2006-03-01.openapi.yaml")
		Expect(err).NotTo(HaveOccurred())
		s, err := spec.Parse("s3.yaml", data, santhosh.NewEngine)
		Expect(err).NotTo(HaveOccurred())
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Name>shop-invoices</Name><Prefix></Prefix><KeyCount>50</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`)
		for i := range 50 {
			fmt.Fprintf(&b, `<Contents><Key>invoice-%04d.pdf</Key><LastModified>2026-11-02T10:00:00.000Z</LastModified>`+
				`<Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, i, 1000+i)
		}
		b.WriteString(`</ListBucketResult>`)
		body := b.String()
		e := spec.Exchange{Method: "GET", EscapedPath: "/", RawQuery: "list-type=2",
			Header: http.Header{"Authorization": {"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261102/eu-west-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=00"}},
			Status: 200, ReplyHeader: http.Header{"Content-Type": {"application/xml"}}, ReplyBody: spec.Body{Data: []byte(body)}}
		Expect(s.Check(e).Reply).To(BeEmpty())
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := s.Check(e)
		runtime.ReadMemStats(&after)
		Expect(got.Request).To(BeEmpty())
		Expect(got.Reply).To(BeEmpty())
		used := float64(after.TotalAlloc-before.TotalAlloc) / float64(len(body))
		Expect(used).To(BeNumerically("<", 12), "memory used, as times the body's size")
	})
})
