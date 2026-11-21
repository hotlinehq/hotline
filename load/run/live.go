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

// liveAPI is the api's name in the proxy's config.
const liveAPI = "live"

// socketShape is a WebSocket shape: which vendor, what an app opens there,
// what each session sends, and how messages are packed.
type socketShape struct {
	vendor, path, what, packing string
	start                       func() (*fakes.LiveVendor, error)
}

// liveShape says whether a shape is a WebSocket one, and which: live-plain,
// live-alone or live-context for Gemini Live, kraken-plain, kraken-alone
// or kraken-context for Kraken's market data.
func liveShape(shape string) (socketShape, bool) {
	vendors := map[string]socketShape{
		"live": {
			vendor: "gemini", path: "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent",
			what:  fmt.Sprintf("Gemini Live sessions, %d audio messages a second each way", int(time.Second/live.Chunk)),
			start: fakes.StartLiveVendor,
		},
		"kraken": {
			vendor: "kraken", path: "/v2",
			what:  fmt.Sprintf("Kraken market data sessions, %d pings a second, each answered by a pong and a book update", int(time.Second/live.Chunk)),
			start: fakes.StartKrakenVendor,
		},
	}
	prefix, packing, _ := strings.Cut(shape, "-")
	s, known := vendors[prefix]
	s.packing = packing
	return s, known && slices.Contains([]string{"plain", "alone", "context"}, packing)
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
	// RecordsDropped and SpansDropped are what the proxy's exporters lost,
	// by the proxy's own count: a queue that overflowed or a send that
	// failed.
	RecordsDropped int64 `json:"recordsDropped"`
	SpansDropped   int64 `json:"spansDropped"`
	// RecordsPerSecond and SpansPerSecond are over the repeat's length.
	RecordsPerSecond float64 `json:"messageRecordsPerSecond"`
	SpansPerSecond   float64 `json:"spansPerSecond"`
}

// LiveScenario says in one line what a live run does.
func LiveScenario(cfg Config) string {
	s, _ := liveShape(cfg.Shape)
	repeats := fmt.Sprintf("%d repeats", cfg.Repeats)
	if cfg.Repeats == 1 {
		repeats = "1 repeat"
	}
	return fmt.Sprintf("scenario: %s (%s, packing %s), %d sessions at once for %s, %s, each direct then through the proxy",
		cfg.Shape, s.what, s.packing, cfg.Sessions, cfg.Duration, repeats)
}

// RunLive does the whole run of a live shape and returns its folder.
func RunLive(cfg Config) (string, error) {
	shape, _ := liveShape(cfg.Shape)
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
	vendor, err := shape.start()
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
	d := liveDriver{k6: k6Path, script: scriptPath, vendor: shape.vendor, packing: shape.packing, tags: tags,
		directURL: "ws://" + vendor.PlainAddr + shape.path,
		proxyURL:  "ws://" + p.addr + "/https://localhost:" + vendorPort + shape.path}
	idle := receiver.Messages()
	warm, err := d.k6run(filepath.Join(work, "warmup.json"), "3s", true, min(cfg.Sessions, 10), "warmup", 0)
	if err != nil {
		return "", fmt.Errorf("warm-up through the proxy failed: %w", err)
	}
	quiet(receiver, idle, int64(warm.Messages()))

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
	if cfg.Profile {
		load := func(summary string) error {
			_, err := d.k6run(summary, cfg.Duration, true, cfg.Sessions, "profile", 0)
			return err
		}
		if err := saveProfiles(cfg, p, filepath.Join(dir, "profile"), tags, load); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// liveTable prints one line per repeat.
func liveTable(w io.Writer, results []LiveResult) {
	_, _ = fmt.Fprintln(w, "repeat  sessions  failed  messages  extra p50 / p95 / p99 (ms)  cpu us/msg  KB/msg  peak MB  KB/session  records/s  spans/s  checked  skipped  unknown  unread  dropped")
	for _, r := range results {
		skipped := int64(0)
		for _, n := range r.Skipped {
			skipped += n
		}
		_, _ = fmt.Fprintf(w, "%6d  %8d  %6d  %8d  %7.2f / %6.2f / %6.2f  %10.1f  %6.2f  %7d  %10.0f  %9.0f  %7.0f  %7d  %7d  %7d  %6d  %7d\n",
			r.Repeat, r.Proxy.Sessions, r.Proxy.Failed+r.Direct.Failed, r.Proxy.Messages(), r.ExtraP50, r.ExtraP95, r.ExtraP99,
			r.CPUMicrosPerMessage, r.AllocatedBytesPerMessage/1024, r.PeakRSSKB/1024, r.SessionKB,
			r.RecordsPerSecond, r.SpansPerSecond, r.Valid+r.Invalid, skipped, r.Unknown, r.Unread, r.RecordsDropped+r.SpansDropped)
	}
}

// liveDriver runs k6 for one live shape.
type liveDriver struct {
	k6, script, vendor, packing string
	tags                        PushTags
	directURL, proxyURL         string
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
	cmd.Env = append(os.Environ(), "URL="+url, "AUTH="+auth, "VENDOR="+d.vendor, "PACKING="+d.packing,
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
	recordsLost0, spansLost0 := receiver.Dropped()
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
	after := quiet(receiver, before, int64(l.Messages()))
	recordsLost1, spansLost1 := receiver.Dropped()
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
		RecordsDropped:           recordsLost1 - recordsLost0,
		SpansDropped:             spansLost1 - spansLost0,
	}, nil
}

// quiet waits until the receiver has a record for each of the want
// messages since before, or for 20 seconds, and says what the records
// held. The proxy sends records every 15 seconds unless 512 wait
// (logInterval), so a repeat's last few hundred can come that late:
// waiting for the counts to stay the same for a second, or five, missed
// them, up to 2 in 100 on the load runner (2026-11-20).
func quiet(receiver *fakes.Receiver, before fakes.Messages, want int64) fakes.Messages {
	now := receiver.Messages()
	for range 20 {
		if now.Records-before.Records >= want {
			return now
		}
		time.Sleep(time.Second)
		now = receiver.Messages()
	}
	return now
}
