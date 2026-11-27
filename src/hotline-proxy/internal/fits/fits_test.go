package fits_test

import (
	"math"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/fits"
)

var _ = Describe("Fitting a number into a smaller type", func() {
	DescribeTable("a byte",
		func(n int, want uint8, fit bool) {
			got, ok := fits.Uint8(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", 0, uint8(0), true),
		Entry("takes the largest byte", math.MaxUint8, uint8(math.MaxUint8), true),
		Entry("refuses one more than a byte holds", math.MaxUint8+1, uint8(0), false),
		Entry("refuses a negative number", -1, uint8(0), false),
	)

	DescribeTable("a port",
		func(n int, want uint16, fit bool) {
			got, ok := fits.Uint16(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", 0, uint16(0), true),
		Entry("takes the largest port", math.MaxUint16, uint16(math.MaxUint16), true),
		Entry("refuses one past the largest port", math.MaxUint16+1, uint16(0), false),
		Entry("refuses a negative number", -1, uint16(0), false),
	)

	DescribeTable("a 32 bit number",
		func(n int, want int32, fit bool) {
			got, ok := fits.Int32(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes the smallest", math.MinInt32, int32(math.MinInt32), true),
		Entry("takes the largest", math.MaxInt32, int32(math.MaxInt32), true),
		Entry("refuses one past the largest", math.MaxInt32+1, int32(0), false),
		Entry("refuses one below the smallest", math.MinInt32-1, int32(0), false),
	)

	DescribeTable("an unsigned 64 bit number",
		func(n int, want uint64, fit bool) {
			got, ok := fits.Uint64(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", 0, uint64(0), true),
		Entry("takes the largest int", math.MaxInt, uint64(math.MaxInt), true),
		Entry("refuses a negative number", -1, uint64(0), false),
	)

	DescribeTable("a 4 byte length",
		func(n int, want uint32, fit bool) {
			got, ok := fits.Uint32(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", 0, uint32(0), true),
		Entry("takes the largest", math.MaxUint32, uint32(math.MaxUint32), true),
		Entry("refuses one past the largest", math.MaxUint32+1, uint32(0), false),
		Entry("refuses a negative number", -1, uint32(0), false),
	)

	DescribeTable("an unsigned number as an int64",
		func(n uint64, want int64, fit bool) {
			got, ok := fits.Int64(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", uint64(0), int64(0), true),
		Entry("takes the largest int64", uint64(math.MaxInt64), int64(math.MaxInt64), true),
		Entry("refuses one past the largest int64", uint64(math.MaxInt64)+1, int64(0), false),
	)

	DescribeTable("an unsigned number as an int",
		func(n uint64, want int, fit bool) {
			got, ok := fits.Int(n)
			Expect(ok).To(Equal(fit))
			Expect(got).To(Equal(want))
		},
		Entry("takes zero", uint64(0), 0, true),
		Entry("takes the largest int", uint64(math.MaxInt), math.MaxInt, true),
		Entry("refuses one past the largest int", uint64(math.MaxInt)+1, 0, false),
	)

	// The helpers sit on paths that run for every exported record, so a
	// call must cost no more than the check written out by hand. The Go
	// compiler says which functions it can copy into their callers.
	It("is inlined by the compiler, so a call costs nothing extra", func() {
		out, err := exec.Command("go", "build", "-gcflags=-m", ".").CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		for _, name := range []string{"Uint8", "Uint16", "Uint32", "Int32", "Uint64", "Int64", "Int"} {
			Expect(string(out)).To(ContainSubstring("can inline "+name), name)
		}
	})
})
