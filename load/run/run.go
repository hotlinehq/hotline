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
	errProcStat      = errors.New("unreadable /proc stat")
)

// Config is what a run needs.
type Config struct {
	// Proxy is the built hotline-proxy program; Spec the SSM spec it checks
	// calls against.
	Proxy string
	Spec  string
	// K6 is the k6 program; empty means the one on the path.
	K6 string
	// OutDir holds one folder per run.
	OutDir string
	// Shape names the call shape: small-json, large-json or slow.
	Shape string
	// Rate is calls a second; Duration is how long each repeat lasts, as k6
	// writes it ("20s").
	Rate     int
	Duration string
	// Repeats is how many times direct and proxy are each run, alternating.
	Repeats int
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
	// the proxy run (from /proc), divided by the calls made, in microseconds.
	CPUMicrosPerCall float64 `json:"proxyCpuMicrosPerCall"`
	// AllocatedBytesPerCall is the growth of the proxy's go.memory.allocated
	// during the run and the 2 seconds after, divided by the calls made.
	AllocatedBytesPerCall float64 `json:"proxyAllocatedBytesPerCall"`
	// PeakRSSKB is the highest resident memory of the proxy process sampled
	// once a second during the proxy run, in kilobytes.
	PeakRSSKB int `json:"proxyPeakRssKb"`
	// OTLPRequests and OTLPBytes are what the receiver got during the proxy
	// run and the 2 seconds after.
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

// call is what k6 sends for a shape.
type call struct{ action, body string }

func callFor(shape string) (call, error) {
	if _, ok := fakes.Shapes()[shape]; !ok {
		return call{}, fmt.Errorf("%w: no call shape called %q", errUnknownShape, shape)
	}
	switch shape {
	case "large-json":
		return call{"AmazonSSM.GetParametersByPath", `{"Path":"/shop"}`}, nil
	case "small-json", "slow":
		return call{"AmazonSSM.GetParameter", `{"Name":"/shop/db-url","WithDecryption":true}`}, nil
	}
	return call{}, fmt.Errorf("%w: %q", errShapeNotWired, shape)
}

// Run does the whole run and returns its folder.
func Run(cfg Config) (string, error) {
	c, err := callFor(cfg.Shape)
	if err != nil {
		return "", err
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
	p, err := startProxy(cfg, work, vendor, receiver)
	if err != nil {
		return "", err
	}
	defer p.stop()

	scriptPath := filepath.Join(work, "load.js")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		return "", err
	}
	_, vendorPort, _ := net.SplitHostPort(vendor.TLSAddr)
	d := driver{k6: k6Path, script: scriptPath, call: c, rate: cfg.Rate}
	d.directURL = "http://" + vendor.PlainAddr + "/"
	d.proxyURL = "http://" + p.addr + "/https://localhost:" + vendorPort + "/"
	// A short warm-up, so connections and caches are not part of the numbers.
	if _, err := d.k6run(filepath.Join(work, "warmup.json"), "3s", d.proxyURL, true, 100); err != nil {
		return "", fmt.Errorf("warm-up through the proxy failed: %w", err)
	}

	var results []Result
	for n := 1; n <= cfg.Repeats; n++ {
		repeat := filepath.Join(dir, fmt.Sprintf("repeat-%d", n))
		if err := os.Mkdir(repeat, 0o750); err != nil {
			return "", err
		}
		direct, err := d.k6run(filepath.Join(repeat, "direct-summary.json"), cfg.Duration, d.directURL, false, cfg.Rate)
		if err != nil {
			return "", fmt.Errorf("direct run %d: %w", n, err)
		}
		r, err := d.measured(p, receiver, filepath.Join(repeat, "proxy-summary.json"), cfg.Duration)
		if err != nil {
			return "", fmt.Errorf("proxy run %d: %w", n, err)
		}
		r.Shape, r.Rate, r.Duration, r.Repeat, r.Direct = cfg.Shape, cfg.Rate, cfg.Duration, n, direct
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
	}
	return dir, nil
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
	directURL, proxyURL string
}

func (d driver) k6run(summary, duration, url string, viaProxy bool, rate int) (Latency, error) {
	auth := "0"
	if viaProxy {
		auth = "1"
	}
	cmd := exec.CommandContext(context.Background(), d.k6, "run", "-q", "--summary-export="+summary, d.script) //nolint:gosec // the k6 the person chose
	cmd.Env = append(os.Environ(), "URL="+url, "BODY="+d.call.body, "ACTION="+d.call.action, "AUTH="+auth,
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

// settle is how long to wait after a run for the proxy's last numbers: its
// telemetry goes every second.
const settle = 2200 * time.Millisecond

// measured runs k6 through the proxy and reads the proxy's own cost.
func (d driver) measured(p *proxyProcess, receiver *fakes.Receiver, summary, duration string) (Result, error) {
	time.Sleep(settle)
	alloc0, ok := receiver.Allocated()
	if !ok {
		return Result{}, errNoAllocated
	}
	reqs0, bytes0 := receiver.Requests(), receiver.Bytes()
	cpu0, err := p.cpuTicks()
	if err != nil {
		return Result{}, err
	}
	sampler := p.sampleMemory()
	l, err := d.k6run(summary, duration, d.proxyURL, true, d.rate)
	peak := sampler()
	if err != nil {
		return Result{}, err
	}
	cpu1, err := p.cpuTicks()
	if err != nil {
		return Result{}, err
	}
	time.Sleep(settle)
	alloc1, _ := receiver.Allocated()
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
func startProxy(cfg Config, work string, vendor *fakes.Vendor, receiver *fakes.Receiver) (*proxyProcess, error) {
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
    endpoint: http://%s
    metricInterval: 1s
apis:
  ssm:
    hosts: [localhost]
    spec: %q
    allowInternalIPs: true
integrations:
  - id: bench-shop
    fingerprintKey: { file: %q }
    apis: [ssm]
`, proxyAddr, adminAddr, receiver.Addr, receiver.Addr, spec, key)
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
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the proxy could not be started: %w", err)
	}
	p := &proxyProcess{cmd: cmd, addr: proxyAddr}
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
// system, in hundredths of a second, from /proc.
func (p *proxyProcess) cpuTicks() (int64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.cmd.Process.Pid))
	if err != nil {
		return 0, err
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

// sampleMemory reads the proxy's resident memory once a second until the
// returned function is called, which gives the highest value in kilobytes.
func (p *proxyProcess) sampleMemory() func() int {
	var mu sync.Mutex
	peak := 0
	stop := make(chan struct{})
	done := make(chan struct{})
	read := func() {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.cmd.Process.Pid))
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
				kb, _ := strconv.Atoi(f[1])
				mu.Lock()
				peak = max(peak, kb)
				mu.Unlock()
			}
		}
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
