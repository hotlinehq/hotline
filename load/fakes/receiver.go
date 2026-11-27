package fakes

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// TelemetryPath is the path in front of /v1/metrics where the proxy sends its
// own numbers (the telemetry destination), so they can be told apart from the
// counts, records and spans of the exporters on the same receiver.
const TelemetryPath = "/telemetry"

// Forward says where the receiver passes the proxy's own numbers on to: a
// base address of an OTLP over HTTP receiver (Grafana Cloud's, ending in
// /otlp) and the instance id and token to sign in with.
type Forward struct {
	URL, Username, Password string
}

// Receiver takes what the proxy exports over OTLP (the OpenTelemetry wire
// format) on HTTP, counts requests and bytes, and keeps nothing but the
// last value of the proxy's memory handed out.
type Receiver struct {
	// Addr is host:port.
	Addr     string
	requests atomic.Int64
	bytes    atomic.Int64
	// records and spans count the sends to /v1/logs and /v1/traces on their
	// own, so a run shows which kind of send grew.
	records counted
	spans   counted
	// messages counts what the WebSocket records said, read as they come.
	messages messageCounts
	// drops keeps the proxy's own count of what its exporters lost, and
	// queueing what it says about its queues.
	drops    drops
	queueing queueing
	// busy answers records and spans with 503, as in an outage.
	busy   atomic.Bool
	server *http.Server

	mu        sync.Mutex
	allocated int64
	seen      bool

	forward         *Forward
	forwarding      sync.WaitGroup
	forwardFailures atomic.Int64
	forwardErr      string
	client          *http.Client
}

// StartReceiver starts the receiver on a free port of 127.0.0.1.
func StartReceiver() (*Receiver, error) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Receiver{Addr: ln.Addr().String()}
	r.server = &http.Server{Handler: http.HandlerFunc(r.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = r.server.Serve(ln) }()
	return r, nil
}

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	r.requests.Add(1)
	counted := &countingReader{r: req.Body}
	var body io.Reader = counted
	if req.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(counted)
		if err != nil {
			_, _ = io.Copy(io.Discard, counted)
			r.bytes.Add(counted.n)
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		body = zr
	}
	data, _ := io.ReadAll(body)
	_, _ = io.Copy(io.Discard, counted)
	r.bytes.Add(counted.n)
	signal := strings.HasSuffix(req.URL.Path, "/v1/logs") || strings.HasSuffix(req.URL.Path, "/v1/traces")
	if signal && r.busy.Load() {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	switch {
	case strings.HasSuffix(req.URL.Path, "/v1/logs"):
		r.records.add(counted.n)
		// Only records of WebSockets are read: decoding every call's record
		// would take processor time from k6, which shares the core.
		var export collogs.ExportLogsServiceRequest
		if bytes.Contains(data, []byte("hotline.websocket.")) && proto.Unmarshal(data, &export) == nil {
			r.messages.readLogs(&export)
		}
	case strings.HasSuffix(req.URL.Path, "/v1/traces"):
		r.spans.add(counted.n)
		var export coltrace.ExportTraceServiceRequest
		if bytes.Contains(data, []byte("websocket ")) && proto.Unmarshal(data, &export) == nil {
			r.messages.readSpans(&export)
		}
	case strings.HasSuffix(req.URL.Path, "/v1/metrics"):
		var export colmetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(data, &export); err != nil {
			http.Error(w, "bad OTLP metrics", http.StatusBadRequest)
			return
		}
		r.readAllocated(&export)
		r.drops.read(&export)
		r.queueing.read(&export)
		if req.URL.Path == TelemetryPath+"/v1/metrics" {
			r.pass(data)
		}
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
}

// Forward sets where the proxy's own numbers are passed on to; nil, or never
// calling it, passes nothing on. Call it before the proxy starts.
func (r *Receiver) Forward(f *Forward) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forward = f
	r.client = &http.Client{Timeout: 10 * time.Second}
}

// pass sends one batch on in the background, so a slow or refusing Grafana
// never slows the proxy's own send or the numbers measured from it.
func (r *Receiver) pass(data []byte) {
	r.mu.Lock()
	f, client := r.forward, r.client
	r.mu.Unlock()
	if f == nil {
		return
	}
	r.forwarding.Add(1)
	go func() {
		defer r.forwarding.Done()
		if err := send(client, f, data); err != nil {
			r.forwardFailures.Add(1)
			r.mu.Lock()
			r.forwardErr = err.Error()
			r.mu.Unlock()
		}
	}()
}

func send(client *http.Client, f *Forward, data []byte) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimSuffix(f.URL, "/")+"/v1/metrics", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.SetBasicAuth(f.Username, f.Password)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the receiver answered %s", resp.Status)
	}
	return nil
}

// ForwardFailures is how many batches were refused or not delivered.
func (r *Receiver) ForwardFailures() int64 { return r.forwardFailures.Load() }

// ForwardError is the last reason a batch was not delivered, or empty.
func (r *Receiver) ForwardError() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.forwardErr
}

// readAllocated keeps the last go.memory.allocated the proxy sent.
func (r *Receiver) readAllocated(export *colmetrics.ExportMetricsServiceRequest) {
	for _, rm := range export.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				if m.GetName() != "go.memory.allocated" {
					continue
				}
				// A total since the start replaces the last one; growth since the
				// last send is added up.
				growth := m.GetSum().GetAggregationTemporality() == metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
				for _, p := range m.GetSum().GetDataPoints() {
					r.mu.Lock()
					if growth {
						r.allocated += p.GetAsInt()
					} else {
						r.allocated = p.GetAsInt()
					}
					r.seen = true
					r.mu.Unlock()
				}
			}
		}
	}
}

// countingReader counts the bytes read through it, as they came over the
// wire, before any gzip is undone.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Requests is how many sends the receiver got.
func (r *Receiver) Requests() int64 { return r.requests.Load() }

// Bytes is how many bytes the sends carried, as they came over the wire.
func (r *Receiver) Bytes() int64 { return r.bytes.Load() }

// Sends is how many sends of one kind the receiver got, and the bytes they
// carried as they came over the wire.
type Sends struct {
	Requests, Bytes int64
}

// counted adds up the sends of one kind.
type counted struct{ requests, bytes atomic.Int64 }

func (c *counted) add(n int64) {
	c.requests.Add(1)
	c.bytes.Add(n)
}

func (c *counted) sends() Sends { return Sends{Requests: c.requests.Load(), Bytes: c.bytes.Load()} }

// Records is the sends of records, to /v1/logs.
func (r *Receiver) Records() Sends { return r.records.sends() }

// Spans is the sends of spans, to /v1/traces.
func (r *Receiver) Spans() Sends { return r.spans.sends() }

// Allocated is the last go.memory.allocated the proxy sent, in bytes, and
// false if none has arrived yet.
func (r *Receiver) Allocated() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allocated, r.seen
}

// Close stops the receiver.
func (r *Receiver) Close() {
	_ = r.server.Close()
	r.forwarding.Wait()
}
