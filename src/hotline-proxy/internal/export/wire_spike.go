package export

// SPIKE: the new encoder wired in for HTTP destinations only, to measure
// the whole proxy under load (docs/otlp-encoder-scope.md). No retries, and
// only the sent and dropped counts the queue keeps today. Not for merging.

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/otlpwire"
)

// wireDest is one HTTP destination on the new path.
type wireDest struct {
	client   *http.Client
	headers  map[string]string
	logsURL  string
	traceURL string
	resource []byte
	interval time.Duration
	size     int

	mu      sync.Mutex
	records [][]byte
	spans   [][]byte
	pool    sync.Pool

	jobs    chan wireJob
	stop    chan struct{}
	stopped chan struct{}
}

type wireJob struct {
	url   string
	items [][]byte
	done  chan struct{}
}

func newWireDest(e config.Exporter, tlsConfig any, resource []attribute.KeyValue) *wireDest {
	base := e.URL.Scheme + "://" + e.URL.Host + strings.TrimSuffix(e.URL.Path, "/")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	w := &wireDest{
		client:   &http.Client{Timeout: 10 * time.Second, Transport: transport},
		headers:  e.Headers,
		logsURL:  base + "/v1/logs",
		traceURL: base + "/v1/traces",
		resource: otlpwire.Resource(resource),
		interval: min(e.LogInterval, e.TraceInterval),
		size:     e.QueueSize,
		jobs:     make(chan wireJob, 64),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	w.pool.New = func() any { b := make([]byte, 0, 2048); return &b }
	go w.run()
	return w
}

func (w *wireDest) buf() []byte { return (*w.pool.Get().(*[]byte))[:0] }

func (w *wireDest) record(at time.Time, sc trace.SpanContext, attrs []attribute.KeyValue) {
	severity := level(attrs)
	b := otlpwire.AppendRecord(w.buf(), otlpwire.Record{
		At: at, Observed: time.Now(), Severity: int(severity), SeverityText: severity.String(),
		EventName: "hotline.request", Context: sc, Attrs: attrs,
	})
	w.mu.Lock()
	w.records = w.add(w.records, b, w.logsURL)
	w.mu.Unlock()
}

func (w *wireDest) trace(sc trace.SpanContext, spans []Span) {
	encoded := make([][]byte, 0, 16)
	for _, s := range spans {
		status := 0
		if s.Failed {
			status = 2
		}
		if s.OK {
			status = 1
		}
		encoded = append(encoded, otlpwire.AppendSpan(w.buf(), otlpwire.Span{
			Trace: sc, ID: s.ID, Parent: s.Parent, Name: s.Name, Kind: s.Kind,
			Start: s.Start, End: s.End, Status: status, Attrs: s.Attrs,
		}))
	}
	w.mu.Lock()
	for _, b := range encoded {
		w.spans = w.add(w.spans, b, w.traceURL)
	}
	w.mu.Unlock()
}

// add queues one item, dropping the oldest when full, and hands a full
// batch to the sender. Called with mu held.
func (w *wireDest) add(items [][]byte, b []byte, url string) [][]byte {
	if len(items) == w.size {
		w.put(items[0])
		items = items[1:]
	}
	items = append(items, b)
	if len(items) >= batchSize {
		select {
		case w.jobs <- wireJob{url: url, items: items}:
			return make([][]byte, 0, batchSize)
		default:
		}
	}
	return items
}

func (w *wireDest) put(b []byte) { w.pool.Put(&b) }

// take empties both queues into jobs.
func (w *wireDest) take() []wireJob {
	w.mu.Lock()
	defer w.mu.Unlock()
	var jobs []wireJob
	if len(w.records) > 0 {
		jobs = append(jobs, wireJob{url: w.logsURL, items: w.records})
		w.records = make([][]byte, 0, batchSize)
	}
	if len(w.spans) > 0 {
		jobs = append(jobs, wireJob{url: w.traceURL, items: w.spans})
		w.spans = make([][]byte, 0, batchSize)
	}
	return jobs
}

func (w *wireDest) run() {
	defer close(w.stopped)
	var batch []byte
	var zipped bytes.Buffer
	zip, _ := gzip.NewWriterLevel(&zipped, gzip.BestSpeed)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	send := func(j wireJob) {
		for len(j.items) > 0 {
			n := min(batchSize, len(j.items))
			batch = otlpwire.Batch(batch[:0], w.resource, "hotline-proxy", j.items[:n])
			for _, b := range j.items[:n] {
				w.put(b)
			}
			j.items = j.items[n:]
			zipped.Reset()
			zip.Reset(&zipped)
			_, _ = zip.Write(batch)
			_ = zip.Close()
			req, _ := http.NewRequest(http.MethodPost, j.url, bytes.NewReader(zipped.Bytes()))
			req.Header.Set("Content-Type", "application/x-protobuf")
			req.Header.Set("Content-Encoding", "gzip")
			for k, v := range w.headers {
				req.Header.Set(k, v)
			}
			if resp, err := w.client.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
		if j.done != nil {
			close(j.done)
		}
	}
	for {
		select {
		case j := <-w.jobs:
			send(j)
		case <-ticker.C:
			for _, j := range w.take() {
				send(j)
			}
		case <-w.stop:
			for {
				select {
				case j := <-w.jobs:
					send(j)
				default:
					for _, j := range w.take() {
						send(j)
					}
					return
				}
			}
		}
	}
}

func (w *wireDest) shutdown(ctx context.Context) error {
	close(w.stop)
	select {
	case <-w.stopped:
	case <-ctx.Done():
	}
	return nil
}
