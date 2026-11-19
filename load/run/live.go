package run

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"hotline-load/fakes"
	"hotline-load/live"
)

//go:embed live.js
var liveScript []byte

var (
	errNoLiveDelays = errors.New("k6 summary has no message_delay: was k6 built with load/k6ws (make k6)?")
	errNoSessions   = errors.New("a live shape needs -sessions above 0")
	errLiveInexact  = errors.New("a live shape needs a proxy that serves its memory exactly (telemetry.pprof)")
	errLiveFailed   = errors.New("sessions failed")
)

// liveAPI is the api's name in the proxy's config, and livePath what an app
// opens on Gemini Live.
const (
	liveAPI  = "live"
	livePath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
)

// livePacking says whether a shape is a WebSocket one, and how it packs
// its messages: live-plain, live-alone or live-context.
func livePacking(shape string) (string, bool) {
	packing, ok := strings.CutPrefix(shape, "live-")
	return packing, ok && slices.Contains([]string{"plain", "alone", "context"}, packing)
}

// LiveLatency is what k6 says about one target in one repeat of a live
// shape: the sessions, the messages each way, and the delay from a chunk
// of audio leaving to the vendor's answer arriving, in milliseconds.
type LiveLatency struct {
	Sessions   int     `json:"sessions"`
	Failed     int     `json:"sessionsFailed"`
	Sent       int     `json:"messagesSent"`
	Received   int     `json:"messagesReceived"`
	Unanswered int     `json:"messagesUnanswered"`
	P50        float64 `json:"p50Ms"`
	P95        float64 `json:"p95Ms"`
	P99        float64 `json:"p99Ms"`
}

// Messages is both ways together.
func (l LiveLatency) Messages() int { return l.Sent + l.Received }

// ReadLiveSummary takes the numbers out of the k6 summary of a live shape.
func ReadLiveSummary(raw []byte) (LiveLatency, error) {
	var s struct {
		Metrics map[string]map[string]float64 `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return LiveLatency{}, fmt.Errorf("k6 summary is not JSON: %w", err)
	}
	d, ok := s.Metrics["message_delay"]
	if !ok {
		return LiveLatency{}, errNoLiveDelays
	}
	count := func(name string) int { return int(s.Metrics[name]["count"]) }
	return LiveLatency{
		Sessions: count("iterations"), Failed: count("sessions_failed"),
		Sent: count("messages_sent"), Received: count("messages_received"), Unanswered: count("messages_unanswered"),
		P50: d["med"], P95: d["p(95)"], P99: d["p(99)"],
	}, nil
}

// LiveResult is one repeat of a live shape, kept as it came.
type LiveResult struct {
	Label    string      `json:"label,omitempty"`
	Shape    string      `json:"shape"`
	Sessions int         `json:"sessions"`
	Duration string      `json:"duration"`
	Repeat   int         `json:"repeat"`
	Direct   LiveLatency `json:"direct"`
	Proxy    LiveLatency `json:"proxy"`
	// ExtraP50, ExtraP95 and ExtraP99 are the proxy's delay per message
	// minus the direct delay, in milliseconds.
	ExtraP50 float64 `json:"extraP50Ms"`
	ExtraP95 float64 `json:"extraP95Ms"`
	ExtraP99 float64 `json:"extraP99Ms"`
	// CPUMicrosPerMessage and AllocatedBytesPerMessage are the proxy's
	// processor time and memory handed out during the proxy run, divided by
	// the messages both ways.
	CPUMicrosPerMessage      float64 `json:"proxyCpuMicrosPerMessage"`
	AllocatedBytesPerMessage float64 `json:"proxyAllocatedBytesPerMessage"`
	// PeakRSSKB is the proxy's highest resident memory during the run, and
	// SessionKB its growth over the memory just before, per open session.
	PeakRSSKB int     `json:"proxyPeakRssKb"`
	SessionKB float64 `json:"proxyKbPerSession"`
	// What the proxy's records said, counted by the receiver: message
	// records, those checked (valid or invalid), those that matched no
	// schema, those skipped by reason, sessions with an unread tail, and
	// every span.
	Records int64            `json:"messageRecords"`
	Valid   int64            `json:"messagesValid"`
	Invalid int64            `json:"messagesInvalid"`
	Unknown int64            `json:"messagesUnknown"`
	Skipped map[string]int64 `json:"messagesSkipped,omitempty"`
	Unread  int64            `json:"sessionsUnread"`
	Spans   int64            `json:"spans"`
	// RecordsPerSecond and SpansPerSecond are over the repeat's length.
	RecordsPerSecond float64 `json:"messageRecordsPerSecond"`
	SpansPerSecond   float64 `json:"spansPerSecond"`
}

// LiveScenario says in one line what a live run does.
func LiveScenario(cfg Config) string {
	packing, _ := livePacking(cfg.Shape)
	repeats := fmt.Sprintf("%d repeats", cfg.Repeats)
	if cfg.Repeats == 1 {
		repeats = "1 repeat"
	}
	return fmt.Sprintf("scenario: %s (Gemini Live sessions, %d audio messages a second each way, packing %s), %d sessions at once for %s, %s, each direct then through the proxy",
		cfg.Shape, int(time.Second/live.Chunk), packing, cfg.Sessions, cfg.Duration, repeats)
}

// RunLive does the whole run of a live shape and returns its folder.
func RunLive(cfg Config) (string, error) {
	packing, _ := livePacking(cfg.Shape)
	if cfg.Sessions <= 0 {
		return "", errNoSessions
	}
	seconds, err := time.ParseDuration(cfg.Duration)
	if err != nil {
		return "", err
	}
	if cfg.Out != nil {
		_, _ = fmt.Fprintln(cfg.Out, LiveScenario(cfg))
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
	vendor, err := fakes.StartLiveVendor()
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
	tags := PushTags{Shape: cfg.Shape, Rate: cfg.Sessions, Label: cfg.Label}
	resource := ""
	if forward := ForwardFromEnv(os.Getenv); forward != nil {
		receiver.Forward(forward)
		resource = TelemetryResource(tags)
	}
	p, err := startProxy(cfg, liveAPI, work, vendor.CertPEM, receiver, resource)
	if err != nil {
		return "", err
	}
	defer KeepProxyLog(work, dir)
	defer p.stop()
	if _, err := TotalAllocated(p.pprofAddr); err != nil {
		return "", errLiveInexact
	}
	scriptPath := filepath.Join(work, "live.js")
	if err := os.WriteFile(scriptPath, liveScript, 0o600); err != nil {
		return "", err
	}
	_, vendorPort, _ := net.SplitHostPort(vendor.TLSAddr)
	d := liveDriver{k6: k6Path, script: scriptPath, packing: packing, tags: tags,
		directURL: "ws://" + vendor.PlainAddr + livePath,
		proxyURL:  "ws://" + p.addr + "/https://localhost:" + vendorPort + livePath}
	if _, err := d.k6run(filepath.Join(work, "warmup.json"), "3s", true, min(cfg.Sessions, 10), "warmup", 0); err != nil {
		return "", fmt.Errorf("warm-up through the proxy failed: %w", err)
	}
	quiet(receiver)

	var results []LiveResult
	for n := 1; n <= cfg.Repeats; n++ {
		repeat := filepath.Join(dir, fmt.Sprintf("repeat-%d", n))
		if err := os.Mkdir(repeat, 0o750); err != nil {
			return "", err
		}
		direct, err := d.k6run(filepath.Join(repeat, "direct-summary.json"), cfg.Duration, false, cfg.Sessions, "measured", n)
		if err != nil {
			return "", fmt.Errorf("direct run %d: %w", n, err)
		}
		r, err := d.measured(p, receiver, filepath.Join(repeat, "proxy-summary.json"), cfg.Duration, cfg.Sessions, n)
		if err != nil {
			return "", fmt.Errorf("proxy run %d: %w", n, err)
		}
		r.Label, r.Shape, r.Sessions, r.Duration, r.Repeat, r.Direct = cfg.Label, cfg.Shape, cfg.Sessions, cfg.Duration, n, direct
		r.ExtraP50, r.ExtraP95, r.ExtraP99 = r.Proxy.P50-direct.P50, r.Proxy.P95-direct.P95, r.Proxy.P99-direct.P99
		r.RecordsPerSecond, r.SpansPerSecond = float64(r.Records)/seconds.Seconds(), float64(r.Spans)/seconds.Seconds()
		raw, err := json.MarshalIndent(r, "", "  ") //nolint:errchkjson // plain numbers and text
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(repeat, "result.json"), raw, 0o600); err != nil {
			return "", err
		}
		results = append(results, r)
	}
	if cfg.Out != nil {
		liveTable(cfg.Out, results)
	}
	return dir, nil
}

// liveTable prints one line per repeat.
func liveTable(w io.Writer, results []LiveResult) {
	_, _ = fmt.Fprintln(w, "repeat  sessions  failed  messages  extra p50 / p95 / p99 (ms)  cpu us/msg  KB/msg  peak MB  KB/session  records/s  spans/s  checked  skipped  unknown  unread")
	for _, r := range results {
		skipped := int64(0)
		for _, n := range r.Skipped {
			skipped += n
		}
		_, _ = fmt.Fprintf(w, "%6d  %8d  %6d  %8d  %7.2f / %6.2f / %6.2f  %10.1f  %6.2f  %7d  %10.0f  %9.0f  %7.0f  %7d  %7d  %7d  %6d\n",
			r.Repeat, r.Proxy.Sessions, r.Proxy.Failed+r.Direct.Failed, r.Proxy.Messages(), r.ExtraP50, r.ExtraP95, r.ExtraP99,
			r.CPUMicrosPerMessage, r.AllocatedBytesPerMessage/1024, r.PeakRSSKB/1024, r.SessionKB,
			r.RecordsPerSecond, r.SpansPerSecond, r.Valid+r.Invalid, skipped, r.Unknown, r.Unread)
	}
}

// liveDriver runs k6 for one live shape.
type liveDriver struct {
	k6, script, packing string
	tags                PushTags
	directURL, proxyURL string
}

func (d liveDriver) k6run(summary, duration string, viaProxy bool, sessions int, phase string, repeat int) (LiveLatency, error) {
	auth, target, url := "0", "direct", d.directURL
	if viaProxy {
		auth, target, url = "1", "proxy", d.proxyURL
	}
	length, err := time.ParseDuration(duration)
	if err != nil {
		return LiveLatency{}, err
	}
	args := []string{"run", "-q", "--summary-export=" + summary}
	args = append(args, PushOptions(os.Getenv("K6_PROMETHEUS_RW_SERVER_URL"), d.tags, target, phase, repeat)...)
	args = append(args, d.script)
	cmd := exec.CommandContext(context.Background(), d.k6, args...) //nolint:gosec // the k6 the person chose
	cmd.Env = append(os.Environ(), "URL="+url, "AUTH="+auth, "PACKING="+d.packing,
		"SESSIONS="+strconv.Itoa(sessions), "SECONDS="+strconv.FormatFloat(length.Seconds(), 'f', -1, 64))
	if out, err := cmd.CombinedOutput(); err != nil {
		return LiveLatency{}, fmt.Errorf("k6 failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(summary) //nolint:gosec // the file k6 just wrote
	if err != nil {
		return LiveLatency{}, err
	}
	l, err := ReadLiveSummary(raw)
	if err == nil && l.Failed > 0 {
		err = fmt.Errorf("%w: %d of %d", errLiveFailed, l.Failed, sessions)
	}
	return l, err
}

// measured runs k6 through the proxy and reads the proxy's own cost and
// what its records said.
func (d liveDriver) measured(p *proxyProcess, receiver *fakes.Receiver, summary, duration string, sessions, repeat int) (LiveResult, error) {
	alloc0, err := TotalAllocated(p.pprofAddr)
	if err != nil {
		return LiveResult{}, err
	}
	before := receiver.Messages()
	rss0 := p.residentKB()
	cpu0, err := p.cpuTicks()
	if err != nil {
		return LiveResult{}, err
	}
	sampler := p.sampleMemory()
	l, err := d.k6run(summary, duration, true, sessions, "measured", repeat)
	peak := sampler()
	if err != nil {
		return LiveResult{}, err
	}
	cpu1, err := p.cpuTicks()
	if err != nil {
		return LiveResult{}, err
	}
	alloc1, err := TotalAllocated(p.pprofAddr)
	if err != nil {
		return LiveResult{}, err
	}
	after := quiet(receiver)
	messages := float64(l.Messages())
	skipped := maps.Clone(after.Skipped)
	for reason, n := range before.Skipped {
		skipped[reason] -= n
		if skipped[reason] == 0 {
			delete(skipped, reason)
		}
	}
	return LiveResult{
		Proxy:                    l,
		CPUMicrosPerMessage:      float64(cpu1-cpu0) * 10000 / messages, // 100 ticks a second
		AllocatedBytesPerMessage: float64(alloc1-alloc0) / messages,
		PeakRSSKB:                peak,
		SessionKB:                float64(peak-rss0) / float64(sessions),
		Records:                  after.Records - before.Records,
		Valid:                    after.Valid - before.Valid,
		Invalid:                  after.Invalid - before.Invalid,
		Unknown:                  after.Unknown - before.Unknown,
		Skipped:                  skipped,
		Unread:                   after.Unread - before.Unread,
		Spans:                    after.Spans - before.Spans,
	}, nil
}

// quiet waits until the proxy has sent the last records, when the
// receiver's count stays the same for a second (at most 20 seconds), and
// says what they held.
func quiet(receiver *fakes.Receiver) fakes.Messages {
	last := receiver.Messages()
	for range 20 {
		time.Sleep(time.Second)
		now := receiver.Messages()
		if now.Records == last.Records && now.Spans == last.Spans && now.Sessions == last.Sessions {
			return now
		}
		last = now
	}
	return last
}
