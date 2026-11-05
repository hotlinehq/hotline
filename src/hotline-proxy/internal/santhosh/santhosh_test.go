package santhosh_test

import (
	"os"
	"runtime"
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

var _ = spectest.Checks("santhosh", santhosh.NewCompiler)

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
		_, err = spec.Parse("ssm.yaml", data, santhosh.NewCompiler)
		runtime.ReadMemStats(&after)
		Expect(err).NotTo(HaveOccurred())
		const mb = 1 << 20
		Expect((after.TotalAlloc - before.TotalAlloc) / mb).To(BeNumerically("<", 400))
	})
})
