package export

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
)

// What the tests in export_test may reach: one queue of strings, to pin
// how it keeps, refuses and sends items.

// BatchSize is how many items one send carries at most.
const BatchSize = batchSize

// TestQueue is a queue of strings that sends with the given function and
// never on its own timer.
type TestQueue struct{ q *queue[string] }

// NewTestQueue is a queue holding at most size items.
func NewTestQueue(size int, send func(context.Context, []string) error) TestQueue {
	counter, _ := noop.NewMeterProvider().Meter("test").Int64Counter("test")
	return TestQueue{newQueue("spans", send, func(context.Context) error { return nil }, queueSetup{
		size: size, interval: time.Hour, logger: slog.New(slog.DiscardHandler),
		sent: counter, dropped: counter, up: &atomic.Int64{},
	})}
}

// Add queues items: all of them, or none.
func (t TestQueue) Add(items ...string) { t.q.add(items...) }

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
