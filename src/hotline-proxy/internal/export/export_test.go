package export

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"hotline-proxy/internal/config"
)

// What the tests in export_test may reach: one queue of strings, to pin
// how it keeps, refuses and sends items.

// BatchSize is how many items one send carries at most.
const BatchSize = batchSize

// TestQueue is a queue of strings that sends with the given function and
// never on its own timer.
type TestQueue struct{ q *queue }

// NewTestQueue is a queue holding at most size items.
func NewTestQueue(size int, send func(context.Context, []string) error) TestQueue {
	counter, _ := noop.NewMeterProvider().Meter("test").Int64Counter("test")
	asText := func(ctx context.Context, items [][]byte) error {
		texts := make([]string, len(items))
		for i, item := range items {
			texts[i] = string(item)
		}
		return send(ctx, texts)
	}
	return TestQueue{newQueue("spans", asText, func(context.Context) error { return nil }, queueSetup{
		size: size, interval: time.Hour, logger: slog.New(slog.DiscardHandler),
		sent: counter, dropped: counter, up: &atomic.Int64{},
	})}
}

// Add queues items: all of them, or none.
func (t TestQueue) Add(items ...string) {
	written := make([][]byte, len(items))
	for i, item := range items {
		written[i] = []byte(item)
	}
	t.q.add(written...)
}

// Waiting is how many items wait.
func (t TestQueue) Waiting() int {
	waiting, _ := t.q.fill()
	return waiting
}

// Refused is how many items, and in how many groups, a full queue refused
// since the last warning.
func (t TestQueue) Refused() (int, int) {
	t.q.mu.Lock()
	defer t.q.mu.Unlock()
	return t.q.crowded.count, t.q.crowded.groups
}

// Flush sends everything that waits.
func (t TestQueue) Flush(ctx context.Context) error { return t.q.ForceFlush(ctx) }

// Shutdown stops the queue.
func (t TestQueue) Shutdown(ctx context.Context) error { return t.q.Shutdown(ctx) }

// LineOf is a call's line as a file exporter writes it, with content:
// full and the proxy's own resource.
func LineOf(c Call) []byte {
	buf := &bytes.Buffer{}
	appendLine(buf, describe(Service{}, nil).Attributes(), c, true)
	return buf.Bytes()
}

// TestTransport sends gzipped records to one collector over HTTP, like a
// exporter does.
type TestTransport struct{ t *httpTransport }

// NewTestTransport sends to the collector at rawURL, with gzip on.
func NewTestTransport(rawURL string) TestTransport {
	u, _ := url.Parse(rawURL)
	return TestTransport{newHTTPTransport(config.Exporter{URL: u, Gzip: true}, nil, "test")}
}

// Sender is one more sender of records over the transport, like the record
// queue or the ledger's batch line of an exporter.
func (t TestTransport) Sender() func(ctx context.Context, body []byte) error {
	return t.SenderWithin(time.Second)
}

// SenderWithin is a sender whose each send may take up to timeout.
func (t TestTransport) SenderWithin(timeout time.Duration) func(ctx context.Context, body []byte) error {
	z := t.t.newZipper()
	return func(ctx context.Context, body []byte) error {
		return sendWithRetry(ctx, t.t, records, z, body, timeout)
	}
}
