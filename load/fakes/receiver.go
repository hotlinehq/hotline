package fakes

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// Receiver takes what the proxy exports over OTLP (the OpenTelemetry wire
// format) on HTTP, counts requests and bytes, and keeps nothing but the
// last value of the proxy's memory handed out.
type Receiver struct {
	// Addr is host:port.
	Addr     string
	requests atomic.Int64
	bytes    atomic.Int64
	server   *http.Server

	mu        sync.Mutex
	allocated int64
	seen      bool
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
	if req.URL.Path == "/v1/metrics" {
		var export colmetrics.ExportMetricsServiceRequest
		if err := proto.Unmarshal(data, &export); err != nil {
			http.Error(w, "bad OTLP metrics", http.StatusBadRequest)
			return
		}
		r.readAllocated(&export)
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
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

// Allocated is the last go.memory.allocated the proxy sent, in bytes, and
// false if none has arrived yet.
func (r *Receiver) Allocated() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allocated, r.seen
}

// Close stops the receiver.
func (r *Receiver) Close() { _ = r.server.Close() }
