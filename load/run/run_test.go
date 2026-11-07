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

	"hotline-load/fakes"
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
// HOTLINE_PROXY to the built program, HOTLINE_SPEC to the SSM spec,
// HOTLINE_S3_SPEC to the S3 spec, and have k6 on the path (or K6).
var _ = DescribeTable("A run with the proxy at a tiny rate",
	func(shape, specVar string) {
		proxy, spec := os.Getenv("HOTLINE_PROXY"), os.Getenv(specVar)
		k6 := os.Getenv("K6")
		if k6 == "" {
			k6 = "k6"
		}
		if _, err := exec.LookPath(k6); err != nil || proxy == "" || spec == "" {
			Skip("needs k6, and HOTLINE_PROXY and " + specVar)
		}
		out := GinkgoT().TempDir()
		dir, err := run.Run(run.Config{
			Proxy: proxy, Spec: spec, K6: k6, OutDir: out,
			Shape: shape, Rate: 5, Duration: "3s", Repeats: 3, Settle: 2 * time.Second,
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
			Expect(strings.TrimSpace(r.Shape)).To(Equal(shape))
		}
		Expect(filepath.Join(dir, "repeat-1", "proxy-summary.json")).To(BeAnExistingFile())
		Expect(filepath.Join(dir, "repeat-1", "direct-summary.json")).To(BeAnExistingFile())
	},
	Entry("finishes an SSM call with no failures and writes every repeat", "small-json", "HOTLINE_SPEC"),
	Entry("finishes an S3 listing in XML with no failures and writes every repeat", "xml", "HOTLINE_S3_SPEC"),
)

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

	It("gives the XML reply an S3 listing of a bucket", func() {
		line, err := run.Scenario(run.Config{Shape: "xml", Rate: 500, Duration: "20s", Repeats: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(line).To(Equal("scenario: xml (S3 ListObjectsV2, reply about 7 KB), " +
			"500 calls a second for 20s, 3 repeats, each direct then through the proxy"))
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

	It("says plainly when the proxy serves no profile pages", func() {
		page := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(page.Close)
		err := run.FetchProfile(page.URL+"/debug/pprof/allocs", filepath.Join(GinkgoT().TempDir(), "x"))
		Expect(err).To(MatchError(ContainSubstring("telemetry.pprof")))
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

	It("says there is no page when the proxy has telemetry.pprof off or lacks it", func() {
		page := httptest.NewServer(http.NotFoundHandler())
		DeferCleanup(page.Close)
		_, err := run.TotalAllocated(page.URL[len("http://"):])
		Expect(err).To(MatchError(ContainSubstring("telemetry.pprof")))
	})

	It("says there is no page when nothing listens", func() {
		_, err := run.TotalAllocated("127.0.0.1:1")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Sending k6's own numbers to Grafana", func() {
	run1 := run.PushTags{Shape: "xml", Rate: 500, Label: "main"}

	It("adds nothing when no push address is set", func() {
		Expect(run.PushOptions("", run1, "proxy", "measured", 2)).To(BeEmpty())
	})

	It("pushes with Prometheus remote write, without the url tag, and tags the run", func() {
		got := run.PushOptions("https://prom.example/api/prom/push", run1, "proxy", "measured", 2)
		Expect(got).To(Equal([]string{
			"-o", "experimental-prometheus-rw",
			"--system-tags=method,status,name,scenario,expected_response",
			"--tag", "shape=xml", "--tag", "rate=500",
			"--tag", "target=proxy", "--tag", "label=main",
			"--tag", "repeat=2", "--tag", "phase=measured",
		}))
	})

	It("leaves the label tag out when there is no label, as k6 refuses an empty tag value", func() {
		got := run.PushOptions("https://prom.example/api/prom/push", run.PushTags{Shape: "xml", Rate: 500}, "direct", "warmup", 0)
		Expect(strings.Join(got, " ")).NotTo(ContainSubstring("label="))
		Expect(got).To(ContainElement("target=direct"))
	})
})

var _ = Describe("Sending the proxy's own numbers to Grafana", func() {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	It("forwards nothing unless the address and the instance id are both set", func() {
		Expect(run.ForwardFromEnv(env(nil))).To(BeNil())
		Expect(run.ForwardFromEnv(env(map[string]string{"GRAFANA_OTLP_ENDPOINT": "https://otlp.example/otlp"}))).To(BeNil())
		Expect(run.ForwardFromEnv(env(map[string]string{"GRAFANA_OTLP_USERNAME": "42"}))).To(BeNil())
	})

	It("takes the address, instance id and token from the environment", func() {
		got := run.ForwardFromEnv(env(map[string]string{
			"GRAFANA_OTLP_ENDPOINT": "https://otlp.example/otlp", "GRAFANA_OTLP_USERNAME": "42", "GRAFANA_OTLP_PASSWORD": "token",
		}))
		Expect(got).NotTo(BeNil())
		Expect(*got).To(Equal(fakes.Forward{URL: "https://otlp.example/otlp", Username: "42", Password: "token"}))
	})

	It("labels the proxy's numbers with the shape, rate and label, and names its instance after them", func() {
		Expect(run.TelemetryResource(run.PushTags{Shape: "xml", Rate: 500, Label: "main"})).To(Equal(
			"    resource:\n      service.name: \"hotline-proxy-load\"\n      service.instance.id: \"xml-500-main\"\n      hotline.load.shape: \"xml\"\n      hotline.load.rate: \"500\"\n      hotline.load.label: \"main\"\n"))
		got := run.TelemetryResource(run.PushTags{Shape: "xml", Rate: 500})
		Expect(got).To(ContainSubstring("service.instance.id: \"xml-500\""))
		Expect(got).NotTo(ContainSubstring("label"))
	})
})

var _ = Describe("Showing the proxy's log as it is written", func() {
	It("puts the prefix in front of each line, as the lines complete", func() {
		var out strings.Builder
		w := run.NewPrefixWriter(&out, "proxy: ")
		_, err := w.Write([]byte("first\nsecond\nthi"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(Equal("proxy: first\nproxy: second\n"))
		_, err = w.Write([]byte("rd\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(out.String()).To(Equal("proxy: first\nproxy: second\nproxy: third\n"))
	})

	It("reports every byte as written, so the proxy never blocks on its log", func() {
		var out strings.Builder
		n, err := run.NewPrefixWriter(&out, "proxy: ").Write([]byte("abc\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(4))
	})
})

var _ = Describe("Keeping the proxy's log", func() {
	It("copies the log into the run folder", func() {
		work, dir := GinkgoT().TempDir(), GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(work, "proxy.log"), []byte("{\"msg\":\"exporter down\"}\n"), 0o600)).To(Succeed())
		run.KeepProxyLog(work, dir)
		saved, err := os.ReadFile(filepath.Join(dir, "proxy.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(saved)).To(ContainSubstring("exporter down"))
	})

	It("copes with no log file", func() {
		Expect(func() { run.KeepProxyLog(GinkgoT().TempDir(), GinkgoT().TempDir()) }).NotTo(Panic())
	})
})

var _ = Describe("How the load test starts the proxy", func() {
	It("asks for the info log level, so its starting and stopping show in the job log", func() {
		args := run.ProxyArgs("hotline-proxy.yaml", "vendor.pem")
		Expect(args).To(ContainElements("--log-level", "info"))
		Expect(args).To(ContainElements("--config", "hotline-proxy.yaml", "--trust-file", "vendor.pem"))
	})
})

var _ = Describe("The proxy's metric interval in the load test", func() {
	It("accepts any interval when the memory is read exactly from the proxy", func() {
		Expect(run.CheckMetricInterval("60s", true)).To(Succeed())
		Expect(run.CheckMetricInterval("1s", true)).To(Succeed())
	})

	It("refuses a long interval when the memory has to come from the proxy's numbers", func() {
		err := run.CheckMetricInterval("60s", false)
		Expect(err).To(MatchError(ContainSubstring("telemetry.pprof")))
		Expect(run.CheckMetricInterval("1s", false)).To(Succeed())
	})

	It("refuses an interval that is not a duration", func() {
		Expect(run.CheckMetricInterval("often", true)).To(MatchError(ContainSubstring("not a duration")))
	})
})

var _ = Describe("Loading many specs into the proxy", func() {
	It("takes the spec of every folder but the one that calls go to, each on a host of its own", func() {
		dir := GinkgoT().TempDir()
		for _, name := range []string{"ssm", "sqs", "kms"} {
			Expect(os.MkdirAll(filepath.Join(dir, name), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, name, "2020-01-01.openapi.yaml"), []byte("openapi: 3.0.0\n"), 0o644)).To(Succeed())
		}
		Expect(os.WriteFile(filepath.Join(dir, "README.md"), []byte("notes"), 0o644)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(dir, "empty"), 0o755)).To(Succeed())

		got, err := run.MoreAPIs(dir, "ssm")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]run.MoreAPI{
			{Name: "kms", Host: "kms.more.test", Spec: filepath.Join(dir, "kms", "2020-01-01.openapi.yaml")},
			{Name: "sqs", Host: "sqs.more.test", Spec: filepath.Join(dir, "sqs", "2020-01-01.openapi.yaml")},
		}))
	})

	It("gives absolute paths, which the proxy needs because it reads relative ones from its config's folder", func() {
		dir := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(dir, "kms"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "kms", "2020-01-01.openapi.yaml"), []byte("openapi: 3.0.0\n"), 0o644)).To(Succeed())
		wd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		rel, err := filepath.Rel(wd, dir)
		Expect(err).NotTo(HaveOccurred())

		got, err := run.MoreAPIs(rel, "ssm")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(1))
		Expect(filepath.IsAbs(got[0].Spec)).To(BeTrue())
		Expect(got[0].Spec).To(Equal(filepath.Join(dir, "kms", "2020-01-01.openapi.yaml")))
	})

	It("refuses a folder with no specs in it", func() {
		_, err := run.MoreAPIs(GinkgoT().TempDir(), "ssm")
		Expect(err).To(MatchError(ContainSubstring("no specs")))
	})

	It("writes the apis and the names the integration lists", func() {
		apis, names := run.MoreYAML([]run.MoreAPI{{Name: "kms", Host: "kms.more.test", Spec: "/s/kms.yaml"}})
		Expect(apis).To(Equal("  kms:\n    hosts: [kms.more.test]\n    spec: \"/s/kms.yaml\"\n    allowInternalIPs: true\n"))
		Expect(names).To(Equal(", kms"))
	})
})

var _ = Describe("Giving the proxy its own cores", func() {
	It("runs the proxy as it is when no cores are named", func() {
		name, args := run.ProxyCommand("", "out/hotline-proxy", []string{"-config", "c.yaml"})
		Expect(name).To(Equal("out/hotline-proxy"))
		Expect(args).To(Equal([]string{"-config", "c.yaml"}))
	})

	It("runs the proxy under taskset on the named cores", func() {
		name, args := run.ProxyCommand("0,1", "out/hotline-proxy", []string{"-config", "c.yaml"})
		Expect(name).To(Equal("taskset"))
		Expect(args).To(Equal([]string{"-c", "0,1", "out/hotline-proxy", "-config", "c.yaml"}))
	})
})
