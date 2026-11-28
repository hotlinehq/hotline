package proxy_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Starting the proxy in a test scenario", func() {
	sut := proxyStartSUT{}

	BeforeEach(func() {
		sut = proxyStartSUT{}
		sut.forScenario()
	})

	It("starts on a new port when another program took its port between two starts", func() {
		Expect(sut.Start()).To(Succeed())
		sut.Stop()
		taken := sut.AnotherProgramTakesTheProxyPort()

		Expect(sut.Start()).To(Succeed())
		Expect(sut.ProxyPort()).NotTo(Equal(taken))
	})

	It("still fails when a scenario took the port on purpose", func() {
		sut.ScenarioTakesTheProxyPortOnPurpose()

		Expect(sut.Start()).To(MatchError(ContainSubstring("address already in use")))
	})
})

// proxyStartSUT is the root sut of a godog scenario, started and stopped
// the way the steps do.
type proxyStartSUT struct {
	scenario *scenario
}

func (sut *proxyStartSUT) forScenario() {
	sut.scenario = newScenario(newReservedPorts(), nil)
	Expect(sut.scenario.open()).WithOffset(1).To(Succeed())
	DeferCleanup(sut.scenario.close)
}

func (sut *proxyStartSUT) Start() error { return sut.scenario.proxy.start() }

// Stop stops the proxy and waits until it has.
func (sut *proxyStartSUT) Stop() { sut.scenario.proxy.settle() }

func (sut *proxyStartSUT) ProxyPort() string { return sut.scenario.proxy.addr }

// AnotherProgramTakesTheProxyPort listens on the port the proxy had, until
// the test ends, and returns its address.
func (sut *proxyStartSUT) AnotherProgramTakesTheProxyPort() string {
	taken := listenLocalOn(sut.scenario.proxy.addr)
	DeferCleanup(taken.Close)
	return taken.Addr().String()
}

// ScenarioTakesTheProxyPortOnPurpose gives the proxy a port another program
// listens on, as a scenario does to see the proxy refuse it.
func (sut *proxyStartSUT) ScenarioTakesTheProxyPortOnPurpose() {
	taken := listenLocal()
	DeferCleanup(taken.Close)
	sut.scenario.proxy.addr = taken.Addr().String()
	sut.scenario.proxy.busy = sut.scenario.proxy.addr
}
