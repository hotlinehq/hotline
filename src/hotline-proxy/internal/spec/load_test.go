package spec_test

import (
	"os"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
)

var _ = Describe("Loading a spec", func() {
	shared := "components:\n  schemas:\n    Count: { type: integer }\n"

	It("checks a body that is only a $ref with the shared shape it names", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json:\n      schema: { $ref: \"#/components/schemas/Count\" }\nresponses: {}\n") + shared)
		header, b := jsonBody(`"x"`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(written(got.Request)).To(Equal("body $ type integer"))
	})

	It("checks a schema that holds a $ref deeper inside", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json:\n      schema: { allOf: [ { $ref: \"#/components/schemas/Count\" } ] }\nresponses: {}\n") + shared)
		header, b := jsonBody(`"x"`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(written(got.Request)).To(Equal("body $ type integer"))
	})

	It("keeps the rules written beside a $ref", func() {
		s := parsed(route("requestBody:\n  content:\n    application/json:\n      schema: { $ref: \"#/components/schemas/Count\", minimum: 5 }\nresponses: {}\n") + shared)
		header, b := jsonBody(`3`)
		got := s.Check(spec.Exchange{Method: "GET", EscapedPath: "/a", Header: header, Body: b})
		Expect(written(got.Request)).To(Equal("body $ minimum 5"))
	})

	It("reports each broken schema, also when the same one is written twice", func() {
		_, err := spec.Parse("test.yaml", []byte(route("parameters:\n  - { name: q, in: query, schema: { type: integr } }\n  - { name: r, in: query, schema: { type: integr } }\nresponses: {}\n")), santhosh.NewCompiler)
		Expect(err).To(MatchError(ContainSubstring("the schema of query q cannot be used")))
		Expect(err).To(MatchError(ContainSubstring("the schema of query r cannot be used")))
		Expect(err).NotTo(MatchError(ContainSubstring("exists")))
	})

	// The SSM spec is the largest vendor spec, with 152 calls and 1,189
	// shared shapes, so it shows how loading grows with a spec's size. It
	// took about 950 MB of short-lived memory before the checks of a
	// shared shape were reused for every $ref to it (Peter, 2026-11-01).
	It("loads the SSM spec without building a new check for every $ref", func() {
		data, err := os.ReadFile("../../../../specs/aws/ssm/2014-11-06.openapi.yaml")
		Expect(err).NotTo(HaveOccurred())
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = spec.Parse("ssm.yaml", data, santhosh.NewCompiler)
		runtime.ReadMemStats(&after)
		Expect(err).NotTo(HaveOccurred())
		const mb = 1 << 20
		Expect((after.TotalAlloc - before.TotalAlloc) / mb).To(BeNumerically("<", 400))
	})
})
