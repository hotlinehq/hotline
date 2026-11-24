//go:build linux || darwin

package pathprobe

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Giving poll a socket", func() {
	It("takes a socket number that fits in 32 bits", func() {
		fd, err := pollable(7)
		Expect(err).NotTo(HaveOccurred())
		Expect(fd).To(Equal(int32(7)))
	})

	It("refuses a socket number beyond 32 bits, so the packet is not sent", func() {
		_, err := pollable(math.MaxInt32 + 1)
		Expect(err).To(MatchError(ErrUnusableSocket))
	})
})
