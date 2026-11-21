package run_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-load/run"
)

var _ = Describe("Reading k6's summary of a live shape", func() {
	It("takes the sessions, the messages each way and the delay percentiles", func() {
		summary := `{"metrics":{"iterations":{"count":20},"sessions_failed":{"count":1},
			"messages_sent":{"count":2600},"messages_received":{"count":2580},"messages_unanswered":{"count":2},
			"message_delay":{"med":0.4,"p(95)":1.2,"p(99)":3.5,"avg":0.5,"max":9}}}`
		got, err := run.ReadLiveSummary([]byte(summary))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(run.LiveLatency{Sessions: 20, Failed: 1, Sent: 2600, Received: 2580, Unanswered: 2, P50: 0.4, P95: 1.2, P99: 3.5}))
		Expect(got.Messages()).To(Equal(5180))
	})

	It("says k6 needs the extension when the summary has no message delays", func() {
		_, err := run.ReadLiveSummary([]byte(`{"metrics":{"iterations":{"count":20}}}`))
		Expect(err).To(MatchError(ContainSubstring("make k6")))
	})

	It("refuses a summary that is not JSON", func() {
		_, err := run.ReadLiveSummary([]byte(`nope`))
		Expect(err).To(MatchError(ContainSubstring("not JSON")))
	})
})

var _ = Describe("A live run that cannot start", func() {
	It("needs a number of sessions", func() {
		_, err := run.Run(run.Config{OutDir: GinkgoT().TempDir(), Shape: "live-plain", Duration: "3s", Repeats: 1})
		Expect(err).To(MatchError(ContainSubstring("-sessions")))
	})

	It("takes a live shape with an unknown packing as an unknown call shape", func() {
		_, err := run.Run(run.Config{OutDir: GinkgoT().TempDir(), Shape: "live-zip", Sessions: 2, Duration: "3s", Repeats: 1})
		Expect(err).To(MatchError(ContainSubstring(`no call shape called "live-zip"`)))
	})

	It("takes a Kraken shape with an unknown packing as an unknown call shape", func() {
		_, err := run.Run(run.Config{OutDir: GinkgoT().TempDir(), Shape: "kraken-zip", Sessions: 2, Duration: "3s", Repeats: 1})
		Expect(err).To(MatchError(ContainSubstring(`no call shape called "kraken-zip"`)))
	})

	It("says plainly when k6 is not installed", func() {
		_, err := run.Run(run.Config{OutDir: GinkgoT().TempDir(), K6: "/no/such/k6", Shape: "live-plain", Sessions: 2, Duration: "3s", Repeats: 1})
		Expect(err).To(MatchError(ContainSubstring("k6 is not installed")))
	})
})

var _ = Describe("The scenario line of a live shape", func() {
	It("names the packing, the message rate and the sessions", func() {
		line := run.LiveScenario(run.Config{Shape: "live-context", Sessions: 50, Duration: "20s", Repeats: 3})
		Expect(line).To(Equal("scenario: live-context (Gemini Live sessions, 25 audio messages a second each way, packing context), " +
			"50 sessions at once for 20s, 3 repeats, each direct then through the proxy"))
	})

	It("says what a Kraken session sends", func() {
		line := run.LiveScenario(run.Config{Shape: "kraken-alone", Sessions: 20, Duration: "20s", Repeats: 1})
		Expect(line).To(Equal("scenario: kraken-alone (Kraken market data sessions, 25 pings a second, each answered by a pong and a book update, packing alone), " +
			"20 sessions at once for 20s, 1 repeat, each direct then through the proxy"))
	})
})

// The whole live run, against the real proxy and the k6 of make k6: set
// HOTLINE_PROXY to the built program and K6_LIVE to that k6.
var _ = DescribeTable("A live run with the proxy and a few sessions",
	func(shape, spec string) {
		proxy, k6 := os.Getenv("HOTLINE_PROXY"), os.Getenv("K6_LIVE")
		if proxy == "" || k6 == "" {
			Skip("needs HOTLINE_PROXY and K6_LIVE, the k6 of make k6")
		}
		dir, err := run.Run(run.Config{
			Proxy: proxy, Spec: spec, K6: k6, OutDir: GinkgoT().TempDir(),
			Shape: shape, Sessions: 3, Duration: "2s", Repeats: 1,
		})
		Expect(err).NotTo(HaveOccurred())
		raw, err := os.ReadFile(filepath.Join(dir, "repeat-1", "result.json")) //nolint:gosec // the run folder the test made
		Expect(err).NotTo(HaveOccurred())
		var r run.LiveResult
		Expect(json.Unmarshal(raw, &r)).To(Succeed())
		Expect(r.Proxy.Sessions).To(Equal(3))
		Expect(r.Proxy.Failed).To(BeZero())
		Expect(r.Proxy.Messages()).To(BeNumerically(">=", 250))
		// Every message is recorded, named and valid against the spec.
		Expect(r.Records).To(BeNumerically(">=", r.Proxy.Messages()))
		Expect(r.Valid).To(Equal(r.Records))
		Expect(r.Unknown).To(BeZero())
		Expect(r.Skipped).To(BeEmpty())
		Expect(r.Spans).To(BeNumerically(">", r.Records))
		Expect(strings.TrimSpace(r.Shape)).To(Equal(shape))
	},
	Entry("records every plain message", "live-plain", "../live/live.openapi.yaml"),
	Entry("records every message packed alone", "live-alone", "../live/live.openapi.yaml"),
	Entry("records every message packed against earlier ones", "live-context", "../live/live.openapi.yaml"),
	Entry("records every plain Kraken message", "kraken-plain", "../live/kraken.openapi.yaml"),
	Entry("records every Kraken message packed alone", "kraken-alone", "../live/kraken.openapi.yaml"),
	Entry("records every Kraken message packed against earlier ones", "kraken-context", "../live/kraken.openapi.yaml"),
)
