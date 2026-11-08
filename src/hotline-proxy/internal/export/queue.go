package export

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// batchSize is how many records or spans one send carries at most.
const batchSize = 512

// queue holds the records or spans for one destination and sends them in
// batches. When it is full, the oldest is dropped and counted, so a call
// never waits for a destination.
type queue[T any] struct {
	export   func(context.Context, []T) error
	close    func(context.Context) error
	endpoint string
	// what names the items in warnings: records or spans.
	what     string
	size     int
	interval time.Duration
	logger   *slog.Logger
	// sent and dropped count what the queue did, with labels naming the
	// exporter and the signal; up is the exporter's state.
	sent    metric.Int64Counter
	dropped metric.Int64Counter
	labels  attribute.Set
	up      *atomic.Int64

	mu    sync.Mutex
	items []T
	// crowded counts items dropped from a full queue, failed those lost
	// in a send the destination refused.
	crowded drops
	failed  drops
	lastErr error
	sendMu  sync.Mutex
	full    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

// queueSetup is what a queue needs besides its sender.
type queueSetup struct {
	endpoint string
	size     int
	interval time.Duration
	logger   *slog.Logger
	sent     metric.Int64Counter
	dropped  metric.Int64Counter
	labels   attribute.Set
	up       *atomic.Int64
}

func newQueue[T any](what string, export func(context.Context, []T) error, closeSender func(context.Context) error, s queueSetup) *queue[T] {
	q := &queue[T]{
		export:   export,
		close:    closeSender,
		endpoint: s.endpoint,
		what:     what,
		size:     s.size,
		interval: s.interval,
		logger:   s.logger,
		sent:     s.sent,
		dropped:  s.dropped,
		labels:   s.labels,
		up:       s.up,
		full:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go q.run()
	return q
}

// add queues an item without waiting.
func (q *queue[T]) add(item T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == q.size {
		q.items = q.items[1:]
		q.crowded.count++
		q.dropped.Add(context.Background(), 1, dropReason(q.labels, "queue_full"))
	}
	q.items = append(q.items, item)
	if len(q.items) >= batchSize {
		select {
		case q.full <- struct{}{}:
		default:
		}
	}
}

func (q *queue[T]) run() {
	defer close(q.stopped)
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-q.full:
		case <-q.stop:
			return
		}
		_ = q.send(context.Background(), false)
	}
}

// send sends what is queued, in batches; all of it when flushing, else one
// batch. A batch the destination refuses is lost and counted: the
// destination has had its retries.
func (q *queue[T]) send(ctx context.Context, all bool) error {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	defer q.warnDropped(all)
	for {
		q.mu.Lock()
		batch := q.items[:min(batchSize, len(q.items))]
		q.items = q.items[len(batch):]
		q.mu.Unlock()
		if len(batch) == 0 {
			return nil
		}
		err := q.export(ctx, batch)
		measured(ctx, q.sent, q.dropped, len(batch), q.labels, err, q.up)
		if err != nil {
			q.mu.Lock()
			q.failed.count += len(batch)
			q.lastErr = err
			q.mu.Unlock()
		}
		if err != nil || !all {
			return err
		}
	}
}

// drops counts lost items until the next warning about them.
type drops struct {
	count  int
	warned time.Time
}

// due says a warning about the drops is due: at most once a minute, and
// always on the last send.
func (d *drops) due(last bool) bool {
	if d.count == 0 || !last && time.Since(d.warned) < time.Minute {
		return false
	}
	d.warned = time.Now()
	return true
}

// warnDropped writes how many items were lost, for each of the two
// reasons.
func (q *queue[T]) warnDropped(last bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.crowded.due(last) {
		q.logger.Warn("export queue full, dropped the oldest "+q.what, "endpoint", q.endpoint, "dropped", q.crowded.count)
		q.crowded.count = 0
	}
	if q.failed.due(last) {
		q.logger.Warn("export failed, dropped the "+q.what+" it held", "endpoint", q.endpoint, "dropped", q.failed.count, "error", q.lastErr.Error())
		q.failed.count = 0
	}
}

// fill is how many items wait, and how many the queue holds at most.
func (q *queue[T]) fill() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items), q.size
}

// ForceFlush sends everything queued.
func (q *queue[T]) ForceFlush(ctx context.Context) error {
	return q.send(ctx, true)
}

// Shutdown stops the queue, sends what is left and closes the sender.
func (q *queue[T]) Shutdown(ctx context.Context) error {
	close(q.stop)
	<-q.stopped
	err := q.ForceFlush(ctx)
	return errors.Join(err, q.close(ctx))
}

// newItemQueue is the queue of one kind of written items for one
// destination. A send writes the waiting items into one request, in a
// buffer the queue keeps, since its sends come one at a time.
func newItemQueue(what string, k kind, t transport, resource []byte, timeout time.Duration, s queueSetup) *queue[[]byte] {
	var body []byte
	send := func(ctx context.Context, items [][]byte) error {
		body = appendBatch(body[:0], resource, items)
		return sendWithRetry(ctx, t, k, body, timeout)
	}
	// The destination closes the transport once both queues are done.
	return newQueue(what, send, func(context.Context) error { return nil }, s)
}
