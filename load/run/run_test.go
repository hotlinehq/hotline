package run_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-load/run"
)

var _ = Describe("Reading k6's summary", func() {
	It("takes the calls, failures and delay percentiles", func() {
		summary := `{"metrics":{"http_reqs":{"count":1000,"rate":50},
			"http_req_failed":{"passes":3,"fails":997,"value":0.003},
			"http_req_duration":{"med":0.8,"p(95)":2.3,"p(99)":5.5,"avg":1,"max":9}}}`
		got, err := run.ReadSummary([]byte(summary))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(run.Latency{Calls: 1000, Failed: 3, P50: 0.8, P95: 2.3, P99: 5.5}))
	})

	It("refuses a file that is not a k6 summary", func() {
		_, err := run.ReadSummary([]byte(`{"metrics":{}}`))
		Expect(err).To(MatchError(ContainSubstring("http_reqs")))
	})
})

var _ = Describe("Reading processor time from ps, for computers without /proc", func() {
	DescribeTable("gives hundredths of a second",
		func(text string, want int64) {
			got, err := run.ParsePSTime(text)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("minutes, seconds and hundredths, as macOS writes it", "0:12.34", int64(1234)),
		Entry("with hours", "1:02:03.50", int64(372350)),
		Entry("with whole seconds, as Linux writes it", "00:00:07", int64(700)),
		Entry("with days", "2-03:04:05", int64(((2*24+3)*3600+4*60+5)*100)),
		Entry("with spaces around it", "  0:00.27 ", int64(27)),
	)

	It("refuses text that is not a time", func() {
		_, err := run.ParsePSTime("soon")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Result folders", func() {
	It("never reuse a folder: each run gets the next number", func() {
		out := GinkgoT().TempDir()
		first, err := run.NextRunDir(out)
		Expect(err).NotTo(HaveOccurred())
		second, err := run.NextRunDir(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Base(first)).To(Equal("run-001"))
		Expect(filepath.Base(second)).To(Equal("run-002"))
	})
})

var _ = Describe("A run that cannot start", func() {
	cfg := func() run.Config {
		return run.Config{OutDir: GinkgoT().TempDir(), Shape: "small-json", Rate: 5, Duration: "3s", Repeats: 1}
	}

	It("says plainly when k6 is not installed", func() {
		c := cfg()
		c.K6 = "/no/such/k6"
		_, err := run.Run(c)
		Expect(err).To(MatchError(ContainSubstring("k6 is not installed")))
	})

	It("says plainly when it does not know the call shape", func() {
		c := cfg()
		c.Shape = "nonsense"
		_, err := run.Run(c)
		Expect(err).To(MatchError(ContainSubstring(`no call shape called "nonsense"`)))
	})

	It("says plainly when the proxy cannot be started", func() {
		c := cfg()
		c.K6 = "k6"
		c.Proxy = "/no/such/hotline-proxy"
		c.Spec = "/no/such/spec.yaml"
		_, err := run.Run(c)
		Expect(err).To(HaveOccurred())
	})
})

// The whole run, against the real proxy and k6. It needs both: set
// HOTLINE_PROXY to the built program, HOTLINE_SPEC to the SSM spec, and have
// k6 on the path (or K6).
var _ = Describe("A run with the proxy at a tiny rate", func() {
	It("finishes with no failed calls and writes every repeat", func() {
		proxy, spec := os.Getenv("HOTLINE_PROXY"), os.Getenv("HOTLINE_SPEC")
		k6 := os.Getenv("K6")
		if k6 == "" {
			k6 = "k6"
		}
		if _, err := exec.LookPath(k6); err != nil || proxy == "" || spec == "" {
			Skip("needs k6, and HOTLINE_PROXY and HOTLINE_SPEC (make load-test sets them)")
		}
		out := GinkgoT().TempDir()
		dir, err := run.Run(run.Config{
			Proxy: proxy, Spec: spec, K6: k6, OutDir: out,
			Shape: "small-json", Rate: 5, Duration: "3s", Repeats: 3, Settle: 2 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Base(dir)).To(Equal("run-001"))
		for _, n := range []string{"1", "2", "3"} {
			raw, err := os.ReadFile(filepath.Join(dir, "repeat-"+n, "result.json")) //nolint:gosec // the run folder the test made
			Expect(err).NotTo(HaveOccurred())
			var r run.Result
			Expect(json.Unmarshal(raw, &r)).To(Succeed())
			Expect(r.Direct.Failed).To(BeZero())
			Expect(r.Proxy.Failed).To(BeZero())
			Expect(r.Proxy.Calls).To(BeNumerically(">=", 10))
			Expect(r.CPUMicrosPerCall).To(BeNumerically(">", 0))
			// Go reports memory handed out in lumps, so a run of 15 calls can
			// show none; the figure is meant for runs of hundreds of calls a second.
			Expect(r.AllocatedBytesPerCall).To(BeNumerically(">=", 0))
			Expect(strings.TrimSpace(r.Shape)).To(Equal("small-json"))
		}
		Expect(filepath.Join(dir, "repeat-1", "proxy-summary.json")).To(BeAnExistingFile())
		Expect(filepath.Join(dir, "repeat-1", "direct-summary.json")).To(BeAnExistingFile())
	})
})

var _ = Describe("The scenario line", func() {
	It("says which call, how big the reply, how fast and how long", func() {
		line, err := run.Scenario(run.Config{Shape: "large-json", Rate: 200, Duration: "20s", Repeats: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(line).To(Equal("scenario: large-json (AmazonSSM.GetParametersByPath, reply about 115 KB), " +
			"200 calls a second for 20s, 3 repeats, each direct then through the proxy"))
	})

	It("gives the medium and huge replies the same call as the large one", func() {
		for shape, size := range map[string]string{"medium-json": "30 KB", "huge-json": "5"} {
			line, err := run.Scenario(run.Config{Shape: shape, Rate: 100, Duration: "20s", Repeats: 3})
			Expect(err).NotTo(HaveOccurred())
			Expect(line).To(ContainSubstring("AmazonSSM.GetParametersByPath"))
			Expect(line).To(ContainSubstring("reply about " + size))
		}
	})

	It("shows a small reply in bytes", func() {
		line, err := run.Scenario(run.Config{Shape: "small-json", Rate: 500, Duration: "20s", Repeats: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(line).To(ContainSubstring("AmazonSSM.GetParameter, reply about 200 bytes"))
		Expect(line).To(ContainSubstring("1 repeat,"))
	})

	It("refuses a shape it does not know", func() {
		_, err := run.Scenario(run.Config{Shape: "nonsense"})
		Expect(err).To(MatchError(ContainSubstring(`no call shape called "nonsense"`)))
	})
})

var _ = Describe("Waiting for the proxy's memory number", func() {
	It("waits longer than the 15 seconds the runtime library keeps its last reading", func() {
		Expect(run.DefaultSettle).To(BeNumerically(">", 15*time.Second))
	})
})

var _ = Describe("Saving a profile from the proxy", func() {
	It("saves what the proxy's profiling page answers into the file", func() {
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("profile of " + r.URL.RequestURI()))
		}))
		DeferCleanup(page.Close)
		file := filepath.Join(GinkgoT().TempDir(), "cpu.pprof")
		Expect(run.FetchProfile(page.URL+"/debug/pprof/allocs", file)).To(Succeed())
		got, err := os.ReadFile(file) //nolint:gosec // the file the test made
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("profile of /debug/pprof/allocs"))
	})

	It("says plainly when the proxy was not built for profiling", func() {
		page := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(page.Close)
		err := run.FetchProfile(page.URL+"/debug/pprof/allocs", filepath.Join(GinkgoT().TempDir(), "x"))
		Expect(err).To(MatchError(ContainSubstring("-tags loadprofile")))
	})
})

var _ = Describe("Reading memory handed out from the proxy's own page", func() {
	It("takes the total allocated bytes from the page, with no wait", func() {
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/debug/vars"))
			_, _ = w.Write([]byte(`{"cmdline":["x"],"memstats":{"Alloc":5,"TotalAlloc":123456789,"Mallocs":7}}`))
		}))
		DeferCleanup(page.Close)
		got, err := run.TotalAllocated(page.URL[len("http://"):])
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(uint64(123456789)))
	})

	It("says there is no page when the proxy was not built for the load test", func() {
		page := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(page.Close)
		_, err := run.TotalAllocated(page.URL[len("http://"):])
		Expect(err).To(MatchError(ContainSubstring("-tags loadprofile")))
	})

	It("says there is no page when nothing listens", func() {
		_, err := run.TotalAllocated("127.0.0.1:1")
		Expect(err).To(HaveOccurred())
	})
})
