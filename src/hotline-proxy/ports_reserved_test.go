package proxy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Reserved ports in test processes running side by side", func() {
	// Go's fuzzing starts its workers at once, so they get process ids one
	// after another (nightly fuzz run 11, 2026-11-25). Only where each
	// process starts is checked, so no port is opened.
	sut := reservedPortsSUT{}

	It("starts processes with back-to-back ids a block of ports apart", func() {
		Expect(sut.FirstPortOf(5019) - sut.FirstPortOf(5018)).To(Equal(reservedBlock))
	})

	It("starts the process after the last block at the first block", func() {
		last := sut.LastProcess()
		Expect(sut.FirstPortOf(last)).To(Equal(reservedCount - reservedBlock))
		Expect(sut.FirstPortOf(last + 1)).To(Equal(0))
	})
})

// reservedPortsSUT is where the reserved ports of a test process start,
// by its process id.
type reservedPortsSUT struct{}

// FirstPortOf is the first port, counted from the start of the reserved
// range, that the process with id pid hands out.
func (reservedPortsSUT) FirstPortOf(pid int) int { return reservedPortsFor(pid).next }

// LastProcess is the process id whose block is the last of the range.
func (reservedPortsSUT) LastProcess() int { return reservedCount/reservedBlock - 1 }
