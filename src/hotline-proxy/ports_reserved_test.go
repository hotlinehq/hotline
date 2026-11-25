package proxy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Reserved ports in test processes running side by side", func() {
	// Go's fuzzing starts its workers at once, so they get process ids one
	// after another (nightly fuzz run 11, 2026-11-25). Only where each
	// process starts is checked, so no port is opened.
	It("starts processes with back-to-back ids a block of ports apart", func() {
		Expect(reservedPortsFor(5019).next - reservedPortsFor(5018).next).To(Equal(reservedBlock))
	})

	It("starts the process after the last block at the first block", func() {
		last := reservedCount/reservedBlock - 1
		Expect(reservedPortsFor(last).next).To(Equal(reservedCount - reservedBlock))
		Expect(reservedPortsFor(last + 1).next).To(Equal(0))
	})
})
