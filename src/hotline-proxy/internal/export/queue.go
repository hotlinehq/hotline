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
// batches. When it is full, what is new is refused and counted, and what
// waits is kept (Peter, 2026-11-09), so a call never waits for a
// destination. The items wait in a ring: a list of fixed length that wraps
// around and is reused, so the queue never grows.
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

	// memory, when above zero, is how many bytes the waiting items may
	// hold, and held how many they hold now.
	memory int64
	held   int64
	// eager sends every item as soon as it comes, not in batches.
	eager bool

	mu sync.Mutex
	// ring holds the waiting items: count of them, from first on, wrapping
	// around the end.
	ring  []T
	first int
	count int
	// batch is the sender's own copy of the items it sends, kept between
	// sends, since sends come one at a time.
	batch []T
	// crowded counts items refused by a full queue, failed those lost in a
	// send the destination refused.
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
	// memory, when above zero, limits the bytes the waiting items hold.
	memory   int64
	interval time.Duration
	// eager sends every item as soon as it comes.
	eager   bool
	logger  *slog.Logger
	sent    metric.Int64Counter
	dropped metric.Int64Counter
	labels  attribute.Set
	up      *atomic.Int64
}

func newQueue[T any](what string, export func(context.Context, []T) error, closeSender func(context.Context) error, s queueSetup) *queue[T] {
	q := &queue[T]{
		export:   export,
		close:    closeSender,
		endpoint: s.endpoint,
		what:     what,
		size:     s.size,
		memory:   s.memory,
		eager:    s.eager,
		interval: s.interval,
		logger:   s.logger,
		sent:     s.sent,
		dropped:  s.dropped,
		labels:   s.labels,
		up:       s.up,
		ring:     make([]T, s.size),
		full:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go q.run()
	return q
}

// add queues items without waiting: all of them, or none when they do not
// all fit, so the spans of one call are never split.
func (q *queue[T]) add(items ...T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	weight := int64(0)
	if q.memory > 0 {
		for _, item := range items {
			weight += weigh(item)
		}
	}
	if len(items) > len(q.ring)-q.count || q.memory > 0 && q.held+weight > q.memory {
		q.crowded.count += len(items)
		q.crowded.groups++
		q.dropped.Add(context.Background(), int64(len(items)), dropReason(q.labels, "queue_full"))
		return
	}
	end := (q.first + q.count) % len(q.ring)
	copied := copy(q.ring[end:], items)
	copy(q.ring, items[copied:])
	q.count += len(items)
	q.held += weight
	if q.count >= batchSize || q.eager {
		select {
		case q.full <- struct{}{}:
		default:
		}
	}
}

// take moves up to one batch of waiting items into the sender's batch, and
// clears their places in the ring so what they point to can be freed. The
// caller holds sendMu.
func (q *queue[T]) take() []T {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(batchSize, q.count)
	q.batch = q.batch[:0]
	for n > 0 {
		part := q.ring[q.first:min(q.first+n, len(q.ring))]
		q.batch = append(q.batch, part...)
		if q.memory > 0 {
			for _, item := range part {
				q.held -= weigh(item)
			}
		}
		clear(part)
		q.first = (q.first + len(part)) % len(q.ring)
		q.count -= len(part)
		n -= len(part)
	}
	return q.batch
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
		batch := q.take()
		if len(batch) == 0 {
			return nil
		}
		err := q.export(ctx, batch)
		// The batch is kept for the next send; let go of what it held.
		clear(batch)
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

// drops counts lost items until the next warning about them, and for a
// full queue the groups they came in: one call's spans are one group.
type drops struct {
	count  int
	groups int
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
		q.logger.Warn("export queue full, dropped the newest "+q.what, "endpoint", q.endpoint, "dropped", q.crowded.count, "calls", q.crowded.groups)
		q.crowded.count, q.crowded.groups = 0, 0
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
	return q.count, q.size
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
func newItemQueue(what string, k kind, t *httpTransport, resource []byte, timeout time.Duration, s queueSetup) *queue[[]byte] {
	var body []byte
	send := func(ctx context.Context, items [][]byte) error {
		body = appendBatch(body[:0], resource, items)
		return sendWithRetry(ctx, t, k, body, timeout)
	}
	// The destination closes the transport once both queues are done.
	return newQueue(what, send, func(context.Context) error { return nil }, s)
}

// weigh is the bytes an item holds: a written item's length, nothing for
// any other kind.
func weigh[T any](item T) int64 {
	b, _ := any(item).([]byte)
	return int64(len(b))
}
