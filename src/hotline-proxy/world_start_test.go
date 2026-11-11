package proxy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Starting the proxy in the test world", func() {
	var w *world

	BeforeEach(func() {
		var err error
		w, err = newWorld()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(w.close)
	})

	It("starts on a new port when another program took its port between two starts", func() {
		Expect(w.start()).To(Succeed())
		w.settle()
		taken := listenLocalOn(w.addr)
		DeferCleanup(taken.Close)

		Expect(w.start()).To(Succeed())
		Expect(w.addr).NotTo(Equal(taken.Addr().String()))
	})

	It("still fails when a scenario took the port on purpose", func() {
		taken := listenLocal()
		DeferCleanup(taken.Close)
		w.addr = taken.Addr().String()
		w.busy = w.addr

		Expect(w.start()).To(MatchError(ContainSubstring("address already in use")))
	})
})
