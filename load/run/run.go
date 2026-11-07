// Package run drives one load run: it starts the fakes and the proxy, runs
// k6 against the vendor directly and through the proxy, and keeps every
// repeat's numbers as they came. Nothing is averaged.
package run

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hotline-load/fakes"
)

//go:embed load.js
var script []byte

var (
	errNoRequests    = errors.New("k6 summary has no http_reqs")
	errUnknownShape  = errors.New("no such call shape")
	errShapeNotWired = errors.New("the call shape is not wired into the runner yet")
	errNoK6          = errors.New("k6 is not installed (get it from https://grafana.com/docs/k6/latest/set-up/install-k6/) or not found")
	errNoAllocated   = errors.New("the proxy has not sent go.memory.allocated to the telemetry destination")
	errProxyStopped  = errors.New("the proxy stopped while starting")
	errProxyNotReady = errors.New("the proxy was not ready within 20 seconds")
	errNoProfile     = errors.New("the proxy has no profiling page: build it with -tags loadprofile")
	errProcStat      = errors.New("cannot read the proxy's processor time from /proc or ps")
	errPSTime        = errors.New("not a time from ps")
)

// Config is what a run needs.
type Config struct {
	// Proxy is the built hotline-proxy program; Spec the spec it checks
	// calls against: SSM's, or S3's for the xml shape.
	Proxy string
	Spec  string
	// K6 is the k6 program; empty means the one on the path.
	K6 string
	// OutDir holds one folder per run.
	OutDir string
	// Shape names the call shape: small-json, medium-json, large-json,
	// huge-json, slow or xml.
	Shape string
	// Rate is calls a second; Duration is how long each repeat lasts, as k6
	// writes it ("20s").
	Rate     int
	Duration string
	// Repeats is how many times direct and proxy are each run, alternating.
	Repeats int
	// Settle is the wait before and after each proxy run for the proxy's
	// memory number; zero means DefaultSettle.
	Settle time.Duration
	// Profile adds one more proxy run after the repeats, not counted in the
	// numbers, that saves a processor and a memory profile into the run
	// folder. The proxy must be built with -tags loadprofile.
	Profile bool
	// Label names what is measured (for example the branch), kept in every
	// result so runs of two builds can be set side by side.
	Label string
	// Out is where the table goes; nil means nowhere.
	Out io.Writer
}

// Latency is what k6 says about one target in one repeat: how many calls,
// how many failed, and the delay in milliseconds at the middle call (p50)
// and the calls slower than 95 and 99 out of 100.
type Latency struct {
	Calls  int     `json:"calls"`
	Failed int     `json:"failed"`
	P50    float64 `json:"p50Ms"`
	P95    float64 `json:"p95Ms"`
	P99    float64 `json:"p99Ms"`
}

// Result is one repeat, kept as it came.
type Result struct {
	Label    string  `json:"label,omitempty"`
	Shape    string  `json:"shape"`
	Rate     int     `json:"ratePerSecond"`
	Duration string  `json:"duration"`
	Repeat   int     `json:"repeat"`
	Direct   Latency `json:"direct"`
	Proxy    Latency `json:"proxy"`
	// ExtraP50, ExtraP95 and ExtraP99 are the proxy's delay minus the direct
	// delay, in milliseconds.
	ExtraP50 float64 `json:"extraP50Ms"`
	ExtraP95 float64 `json:"extraP95Ms"`
	ExtraP99 float64 `json:"extraP99Ms"`
	// CPUMicrosPerCall is the processor time the proxy process used during
	// the proxy run (from /proc, or ps on a Mac), divided by the calls made, in microseconds.
	CPUMicrosPerCall float64 `json:"proxyCpuMicrosPerCall"`
	// AllocatedBytesPerCall is the growth of the proxy's go.memory.allocated
	// during the run and the 16 seconds after, divided by the calls made.
	AllocatedBytesPerCall float64 `json:"proxyAllocatedBytesPerCall"`
	// PeakRSSKB is the highest resident memory of the proxy process sampled
	// once a second during the proxy run, in kilobytes.
	PeakRSSKB int `json:"proxyPeakRssKb"`
	// OTLPRequests and OTLPBytes are what the receiver got during the proxy
	// run and the wait after it.
	OTLPRequests int64 `json:"otlpRequests"`
	OTLPBytes    int64 `json:"otlpBytes"`
}

// ReadSummary takes the numbers out of a k6 summary file.
func ReadSummary(raw []byte) (Latency, error) {
	var s struct {
		Metrics map[string]map[string]float64 `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return Latency{}, fmt.Errorf("k6 summary is not JSON: %w", err)
	}
	reqs, ok := s.Metrics["http_reqs"]
	if !ok {
		return Latency{}, errNoRequests
	}
	d := s.Metrics["http_req_duration"]
	l := Latency{Calls: int(reqs["count"]), P50: d["med"], P95: d["p(95)"], P99: d["p(99)"]}
	if f, ok := s.Metrics["http_req_failed"]; ok {
		l.Failed = int(f["passes"])
	}
	return l, nil
}

// NextRunDir makes the next folder, run-001, run-002 and so on, so no run
// ever overwrites another.
func NextRunDir(out string) (string, error) {
	if err := os.MkdirAll(out, 0o750); err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		dir := filepath.Join(out, fmt.Sprintf("run-%03d", n))
		err := os.Mkdir(dir, 0o750)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
}

// call is what k6 sends for a shape: the API the proxy knows it as, the
// method, what follows the vendor's address (the path and query), the
// service the request is signed for, and for SSM the action and body. name
// is how the scenario line names the call.
type call struct{ api, method, path, service, action, body, name string }

func callFor(shape string) (call, error) {
	if _, ok := fakes.Shapes()[shape]; !ok {
		return call{}, fmt.Errorf("%w: no call shape called %q", errUnknownShape, shape)
	}
	ssm := func(action, body string) call {
		return call{api: "ssm", method: http.MethodPost, service: "ssm", action: action, body: body, name: action}
	}
	switch shape {
	case "medium-json", "large-json", "huge-json":
		return ssm("AmazonSSM.GetParametersByPath", `{"Path":"/shop"}`), nil
	case "small-json", "slow":
		return ssm("AmazonSSM.GetParameter", `{"Name":"/shop/db-url","WithDecryption":true}`), nil
	case "xml":
		return call{api: "s3", method: http.MethodGet, path: "?list-type=2&prefix=invoice-", service: "s3", name: "S3 ListObjectsV2"}, nil
	}
	return call{}, fmt.Errorf("%w: %q", errShapeNotWired, shape)
}

// Scenario says in one line what a run does: the call, the size of the
// reply, the rate, how long each repeat lasts and how many there are.
func Scenario(cfg Config) (string, error) {
	c, err := callFor(cfg.Shape)
	if err != nil {
		return "", err
	}
	size := len(fakes.Shapes()[cfg.Shape].Body)
	reply := fmt.Sprintf("%d bytes", (size+50)/100*100)
	if size >= 1000 {
		reply = fmt.Sprintf("%d KB", size/1000)
	}
	repeats := fmt.Sprintf("%d repeats", cfg.Repeats)
	if cfg.Repeats == 1 {
		repeats = "1 repeat"
	}
	return fmt.Sprintf("scenario: %s (%s, reply about %s), %d calls a second for %s, %s, each direct then through the proxy",
		cfg.Shape, c.name, reply, cfg.Rate, cfg.Duration, repeats), nil
}

// Run does the whole run and returns its folder.
func Run(cfg Config) (string, error) {
	c, err := callFor(cfg.Shape)
	if err != nil {
		return "", err
	}
	if line, err := Scenario(cfg); err == nil && cfg.Out != nil {
		_, _ = fmt.Fprintln(cfg.Out, line)
	}
	k6 := cfg.K6
	if k6 == "" {
		k6 = "k6"
	}
	k6Path, err := exec.LookPath(k6)
	if err != nil {
		return "", fmt.Errorf("%w at %s", errNoK6, k6)
	}
	dir, err := NextRunDir(cfg.OutDir)
	if err != nil {
		return "", err
	}
	vendor, err := fakes.StartVendor(fakes.Shapes()[cfg.Shape])
	if err != nil {
		return "", err
	}
	defer vendor.Close()
	receiver, err := fakes.StartReceiver()
	if err != nil {
		return "", err
	}
	defer receiver.Close()

	work, err := os.MkdirTemp("", "hotline-load-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	runID := os.Getenv("GITHUB_RUN_ID")
	if runID == "" {
		runID = "local"
	}
	tags := PushTags{RunID: runID, Shape: cfg.Shape, Rate: cfg.Rate, Label: cfg.Label}
	resource := ""
	if forward := ForwardFromEnv(os.Getenv); forward != nil {
		receiver.Forward(forward)
		resource = TelemetryResource(tags)
	}
	p, err := startProxy(cfg, c.api, work, vendor, receiver, resource)
	if err != nil {
		return "", err
	}
	defer p.stop()

	scriptPath := filepath.Join(work, "load.js")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		return "", err
	}
	_, vendorPort, _ := net.SplitHostPort(vendor.TLSAddr)
	d := driver{k6: k6Path, script: scriptPath, call: c, rate: cfg.Rate, settle: cfg.Settle, tags: tags}
	if d.settle == 0 {
		d.settle = DefaultSettle
	}
	_, probe := TotalAllocated(p.pprofAddr)
	d.exact = probe == nil
	d.directURL = "http://" + vendor.PlainAddr + "/"
	d.proxyURL = "http://" + p.addr + "/https://localhost:" + vendorPort + "/"
	// A short warm-up, so connections and caches are not part of the numbers.
	if _, err := d.k6run(filepath.Join(work, "warmup.json"), "3s", d.proxyURL, true, 100, "warmup", 0); err != nil {
		return "", fmt.Errorf("warm-up through the proxy failed: %w", err)
	}

	var results []Result
	for n := 1; n <= cfg.Repeats; n++ {
		repeat := filepath.Join(dir, fmt.Sprintf("repeat-%d", n))
		if err := os.Mkdir(repeat, 0o750); err != nil {
			return "", err
		}
		direct, err := d.k6run(filepath.Join(repeat, "direct-summary.json"), cfg.Duration, d.directURL, false, cfg.Rate, "measured", n)
		if err != nil {
			return "", fmt.Errorf("direct run %d: %w", n, err)
		}
		r, err := d.measured(p, receiver, filepath.Join(repeat, "proxy-summary.json"), cfg.Duration, n)
		if err != nil {
			return "", fmt.Errorf("proxy run %d: %w", n, err)
		}
		r.Label, r.Shape, r.Rate, r.Duration, r.Repeat, r.Direct = cfg.Label, cfg.Shape, cfg.Rate, cfg.Duration, n, direct
		r.ExtraP50, r.ExtraP95, r.ExtraP99 = r.Proxy.P50-direct.P50, r.Proxy.P95-direct.P95, r.Proxy.P99-direct.P99
		raw, err := json.MarshalIndent(r, "", "  ") //nolint:errchkjson // plain numbers and text; a NaN would only come from zero calls
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(repeat, "result.json"), raw, 0o600); err != nil {
			return "", err
		}
		results = append(results, r)
	}
	if cfg.Out != nil {
		table(cfg.Out, results)
		if n := receiver.ForwardFailures(); n > 0 {
			fmt.Fprintf(cfg.Out, "warning: %d sends of the proxy's own numbers to Grafana failed, the last: %s\n", n, receiver.ForwardError())
		}
	}
	if cfg.Profile {
		if err := d.profileRun(p, filepath.Join(dir, "profile"), cfg.Duration); err != nil {
			return "", fmt.Errorf("profile run: %w", err)
		}
	}
	return dir, nil
}

// profileRun runs the proxy once more under load and saves a processor
// profile of the middle of the run and the memory profile after it.
func (d driver) profileRun(p *proxyProcess, folder, duration string) error {
	if err := os.Mkdir(folder, 0o750); err != nil {
		return err
	}
	length, err := time.ParseDuration(duration)
	if err != nil {
		return err
	}
	seconds := max(1, int(length.Seconds())-4)
	cpu := make(chan error, 1)
	go func() {
		time.Sleep(2 * time.Second)
		cpu <- FetchProfile(fmt.Sprintf("http://%s/debug/pprof/profile?seconds=%d", p.pprofAddr, seconds), filepath.Join(folder, "cpu.pprof"))
	}()
	if _, err := d.k6run(filepath.Join(folder, "proxy-summary.json"), duration, d.proxyURL, true, d.rate, "profile", 0); err != nil {
		return err
	}
	if err := <-cpu; err != nil {
		return err
	}
	return FetchProfile("http://"+p.pprofAddr+"/debug/pprof/allocs", filepath.Join(folder, "allocs.pprof"))
}

// TotalAllocated is the proxy's total bytes handed out so far, read from the
// page a proxy built with -tags loadprofile serves at addr. It is exact at
// once, unlike the telemetry number that lags up to 15 seconds.
func TotalAllocated(addr string) (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/debug/vars", http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w (%s answered %s)", errNoProfile, addr, resp.Status)
	}
	var vars struct {
		Memstats struct {
			TotalAlloc uint64
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&vars); err != nil {
		return 0, fmt.Errorf("%s/debug/vars is not JSON: %w", addr, err)
	}
	return vars.Memstats.TotalAlloc, nil
}

// FetchProfile saves what a profiling page answers into a file.
func FetchProfile(url, file string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w (%s answered %s)", errNoProfile, url, resp.Status)
	}
	out, err := os.Create(file) //nolint:gosec // a file in the run folder
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// table prints one line per repeat.
func table(w io.Writer, results []Result) {
	_, _ = fmt.Fprintln(w, "repeat  calls  failed  extra p50 / p95 / p99 (ms)   cpu us/call  allocated KB/call  peak memory MB")
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "%6d  %5d  %6d  %8.2f / %6.2f / %6.2f   %11.0f  %17.1f  %14d\n",
			r.Repeat, r.Proxy.Calls, r.Proxy.Failed+r.Direct.Failed, r.ExtraP50, r.ExtraP95, r.ExtraP99,
			r.CPUMicrosPerCall, r.AllocatedBytesPerCall/1024, r.PeakRSSKB/1024)
	}
}

// driver runs k6 for one call shape.
type driver struct {
	k6, script          string
	call                call
	rate                int
	settle              time.Duration
	tags                PushTags
	directURL, proxyURL string
	// exact says the proxy serves its own memory number, so no waiting.
	exact bool
}

func (d driver) k6run(summary, duration, url string, viaProxy bool, rate int, phase string, repeat int) (Latency, error) {
	auth, target := "0", "direct"
	if viaProxy {
		auth, target = "1", "proxy"
	}
	args := []string{"run", "-q", "--summary-export=" + summary}
	args = append(args, PushOptions(os.Getenv("K6_PROMETHEUS_RW_SERVER_URL"), d.tags, target, phase, repeat)...)
	args = append(args, d.script)
	cmd := exec.CommandContext(context.Background(), d.k6, args...) //nolint:gosec // the k6 the person chose
	cmd.Env = append(os.Environ(), "URL="+url+d.call.path, "METHOD="+d.call.method, "SERVICE="+d.call.service,
		"BODY="+d.call.body, "ACTION="+d.call.action, "AUTH="+auth,
		"RATE="+strconv.Itoa(rate), "DURATION="+duration)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Latency{}, fmt.Errorf("k6 failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(summary) //nolint:gosec // the file k6 just wrote
	if err != nil {
		return Latency{}, err
	}
	return ReadSummary(raw)
}

// PushTags says which run the numbers sent to Grafana belong to.
type PushTags struct {
	RunID, Shape, Label string
	Rate                int
}

// PushOptions are the extra k6 options that send k6's own numbers to Grafana
// by Prometheus remote write. With no push address there are none, so local
// runs behave as before. The url tag is left out: the proxy's port changes on
// every run, and each new url would make a new series.
func PushOptions(pushURL string, t PushTags, target, phase string, repeat int) []string {
	if pushURL == "" {
		return nil
	}
	opts := []string{
		"-o", "experimental-prometheus-rw",
		"--system-tags=method,status,name,scenario,expected_response",
		"--tag", "testid=" + t.RunID, "--tag", "shape=" + t.Shape, "--tag", "rate=" + strconv.Itoa(t.Rate),
		"--tag", "target=" + target,
	}
	// k6 refuses a tag with an empty value, and a run without a label has none.
	if t.Label != "" {
		opts = append(opts, "--tag", "label="+t.Label)
	}
	return append(opts, "--tag", "repeat="+strconv.Itoa(repeat), "--tag", "phase="+phase)
}

// ForwardFromEnv says where the proxy's own numbers go on to, from
// GRAFANA_OTLP_ENDPOINT (Grafana Cloud's OpenTelemetry address, ending in
// /otlp), GRAFANA_OTLP_USERNAME (the instance id) and GRAFANA_OTLP_PASSWORD
// (a token that may write metrics). Without the address and the instance id
// it is nil, and nothing is sent.
func ForwardFromEnv(getenv func(string) string) *fakes.Forward {
	url, user := getenv("GRAFANA_OTLP_ENDPOINT"), getenv("GRAFANA_OTLP_USERNAME")
	if url == "" || user == "" {
		return nil
	}
	return &fakes.Forward{URL: url, Username: user, Password: getenv("GRAFANA_OTLP_PASSWORD")}
}

// TelemetryResource is the resource section of the proxy's telemetry setting
// (two levels in), naming the run, so its numbers can be told apart in Grafana.
func TelemetryResource(t PushTags) string {
	var b strings.Builder
	b.WriteString("    resource:\n")
	fmt.Fprintf(&b, "      service.name: %q\n", "hotline-proxy-load")
	fmt.Fprintf(&b, "      hotline.load.testid: %q\n", t.RunID)
	fmt.Fprintf(&b, "      hotline.load.shape: %q\n", t.Shape)
	fmt.Fprintf(&b, "      hotline.load.rate: %q\n", strconv.Itoa(t.Rate))
	if t.Label != "" {
		fmt.Fprintf(&b, "      hotline.load.label: %q\n", t.Label)
	}
	return b.String()
}

// DefaultSettle is how long to wait before and after each proxy run for the
// proxy's memory number. The proxy sends its telemetry every second, but the
// OpenTelemetry Go runtime library re-reads Go's counters at most every 15
// seconds, so a shorter wait can read a stale value and lose part of the
// memory handed out (the cause of the 90, 35 and 45 KB per call that the same
// load showed in three repeats).
const DefaultSettle = 16 * time.Second

// measured runs k6 through the proxy and reads the proxy's own cost.
func (d driver) measured(p *proxyProcess, receiver *fakes.Receiver, summary, duration string, repeat int) (Result, error) {
	allocated := func() (uint64, bool) {
		if d.exact {
			n, err := TotalAllocated(p.pprofAddr)
			return n, err == nil
		}
		n, ok := receiver.Allocated()
		return uint64(n), ok //nolint:gosec // a byte count, never negative
	}
	if !d.exact {
		time.Sleep(d.settle)
	}
	alloc0, ok := allocated()
	if !ok {
		return Result{}, errNoAllocated
	}
	reqs0, bytes0 := receiver.Requests(), receiver.Bytes()
	cpu0, err := p.cpuTicks()
	if err != nil {
		return Result{}, err
	}
	sampler := p.sampleMemory()
	l, err := d.k6run(summary, duration, d.proxyURL, true, d.rate, "measured", repeat)
	peak := sampler()
	if err != nil {
		return Result{}, err
	}
	cpu1, err := p.cpuTicks()
	if err != nil {
		return Result{}, err
	}
	if !d.exact {
		time.Sleep(d.settle)
	}
	alloc1, _ := allocated()
	calls := float64(l.Calls)
	return Result{
		Proxy:                 l,
		CPUMicrosPerCall:      float64(cpu1-cpu0) * 10000 / calls, // 100 ticks a second
		AllocatedBytesPerCall: float64(alloc1-alloc0) / calls,
		PeakRSSKB:             peak,
		OTLPRequests:          receiver.Requests() - reqs0,
		OTLPBytes:             receiver.Bytes() - bytes0,
	}, nil
}

// proxyProcess is the running proxy.
type proxyProcess struct {
	cmd  *exec.Cmd
	addr string
	// pprofAddr is where the proxy serves profiles, when asked to.
	pprofAddr string
}

func freeAddr() (string, error) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String(), nil
}

// startProxy writes the proxy's config, starts it and waits until it is
// ready for calls.
func startProxy(cfg Config, api, work string, vendor *fakes.Vendor, receiver *fakes.Receiver, resource string) (*proxyProcess, error) {
	proxyAddr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	adminAddr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	cert := filepath.Join(work, "vendor.pem")
	key := filepath.Join(work, "fp.key")
	conf := filepath.Join(work, "hotline-proxy.yaml")
	spec, err := filepath.Abs(cfg.Spec)
	if err != nil {
		return nil, err
	}
	yaml := fmt.Sprintf(`listen:
  proxy: %q
  admin: %q
exporters:
  - otlp:
      protocol: http/protobuf
      endpoint: http://%s
telemetry:
  otlp:
    protocol: http/protobuf
    endpoint: http://%s%s
    metricInterval: 1s
%sapis:
  %s:
    hosts: [localhost]
    spec: %q
    allowInternalIPs: true
integrations:
  - id: bench-shop
    fingerprintKey: { file: %q }
    apis: [%s]
`, proxyAddr, adminAddr, receiver.Addr, receiver.Addr, fakes.TelemetryPath, resource, api, spec, key, api)
	for path, data := range map[string][]byte{cert: vendor.CertPEM, key: []byte("hotline-load-test-fingerprint-key"), conf: []byte(yaml)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(context.Background(), cfg.Proxy, "run", "--config", conf, "--trust-file", cert, "--log-level", "warn", "--drain-delay", "0s") //nolint:gosec // the proxy the person built
	logFile, err := os.Create(filepath.Join(work, "proxy.log"))                                                                                              //nolint:gosec // in our own temporary folder
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// A proxy built with -tags loadprofile serves memory and profile pages
	// here; any other build ignores the variable.
	pprofAddr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	cmd.Env = append(os.Environ(), "HOTLINE_LOAD_PPROF_ADDR="+pprofAddr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the proxy could not be started: %w", err)
	}
	p := &proxyProcess{cmd: cmd, addr: proxyAddr, pprofAddr: pprofAddr}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			log, _ := os.ReadFile(logFile.Name())
			return nil, fmt.Errorf("%w (%w): %s", errProxyStopped, err, strings.TrimSpace(string(log)))
		default:
		}
		if ready(adminAddr) {
			return p, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.stop()
	return nil, errProxyNotReady
}

// ready says whether the admin port answers /readyz with 200.
func ready(adminAddr string) bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+adminAddr+"/readyz", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *proxyProcess) stop() {
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
	}
}

// cpuTicks is the processor time the proxy has used so far, user and
// system, in hundredths of a second. It comes from /proc where there is
// one (Linux), else from ps (macOS).
func (p *proxyProcess) cpuTicks() (int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.cmd.Process.Pid))
	if err != nil {
		out, psErr := exec.CommandContext(context.Background(), "ps", "-o", "time=", "-p", strconv.Itoa(p.cmd.Process.Pid)).Output() //nolint:gosec // ps with our own child's number
		if psErr != nil {
			return 0, fmt.Errorf("%w: %w", errProcStat, psErr)
		}
		return ParsePSTime(string(out))
	}
	// The name in brackets may hold spaces; the fields count from after it.
	rest := string(raw[strings.LastIndexByte(string(raw), ')')+2:])
	f := strings.Fields(rest)
	if len(f) < 13 {
		return 0, errProcStat
	}
	user, _ := strconv.ParseInt(f[11], 10, 64)
	system, _ := strconv.ParseInt(f[12], 10, 64)
	return user + system, nil
}

// ParsePSTime reads the time column of ps ("0:12.34", "1:02:03.50",
// "00:00:07" or "2-03:04:05") as hundredths of a second.
func ParsePSTime(text string) (int64, error) {
	text = strings.TrimSpace(text)
	var days int64
	if d, rest, ok := strings.Cut(text, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errPSTime, text)
		}
		days, text = n, rest
	}
	var seconds float64
	for _, part := range strings.Split(text, ":") {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", errPSTime, text)
		}
		seconds = seconds*60 + n
	}
	return int64(math.Round((float64(days)*86400 + seconds) * 100)), nil
}

// residentKB is the proxy's resident memory now, in kilobytes: from
// /proc where there is one (Linux), else from ps (macOS).
func (p *proxyProcess) residentKB() int {
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.cmd.Process.Pid)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
				kb, _ := strconv.Atoi(f[1])
				return kb
			}
		}
		return 0
	}
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "rss=", "-p", strconv.Itoa(p.cmd.Process.Pid)).Output() //nolint:gosec // ps with our own child's number
	if err != nil {
		return 0
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return kb
}

// sampleMemory reads the proxy's resident memory once a second until the
// returned function is called, which gives the highest value in kilobytes.
func (p *proxyProcess) sampleMemory() func() int {
	var mu sync.Mutex
	peak := 0
	stop := make(chan struct{})
	done := make(chan struct{})
	read := func() {
		kb := p.residentKB()
		mu.Lock()
		peak = max(peak, kb)
		mu.Unlock()
	}
	go func() {
		defer close(done)
		read()
		for {
			select {
			case <-stop:
				read()
				return
			case <-time.After(time.Second):
				read()
			}
		}
	}()
	return func() int {
		close(stop)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}
