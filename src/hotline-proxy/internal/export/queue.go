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

	"hotline-proxy/internal/queuefiles"
)

// batchSize is how many records or spans one send carries at most.
const batchSize = 512

// queue holds the records or spans for one exporter and sends them in
// batches. When it is full, what is new is refused and counted, and what
// waits is kept (Peter, 2026-11-09), so a call never waits for a
// exporter. The items wait in a ring: a list of fixed length that wraps
// around and is reused, so the queue never grows.
type Queue struct {
	export   func(context.Context, [][]byte) error
	close    func(context.Context) error
	endpoint string
	// signalName names the signal in warnings: records or spans.
	signalName string
	size       int
	interval   time.Duration
	logger     *slog.Logger
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
	// disk, when set, takes what no longer fits in memory.
	disk *diskQueue

	mu sync.Mutex
	// ring holds the waiting items: count of them, from first on, wrapping
	// around the end.
	ring [][]byte
	// queuedAt is when each item in the ring was queued, at the same place.
	queuedAt []time.Time
	first    int
	count    int
	// batch is the sender's own copy of the items it sends, kept between
	// sends, since sends come one at a time.
	batch [][]byte
	// crowded counts items refused by a full queue, failed those lost in a
	// send the exporter refused.
	crowded drops
	failed  drops
	lastErr error
	sendMu  sync.Mutex
	full    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

// QueueSetup is what a queue needs: where it sends, how many items it
// holds and how often it sends, and where it reports what it did. Send,
// Size, Interval, Logger, Counts and ExporterUp must be set. A zero
// Memory is no limit on bytes, a false Eager sends in batches, and an
// empty Close closes nothing.
type QueueSetup struct {
	// ItemName names the items in warnings: records, spans or calls.
	ItemName string
	// ExporterName names the exporter in warnings: its endpoint, its
	// folder, or console.
	ExporterName string
	// Size is how many items wait at most.
	Size int
	// Memory, when above zero, limits the bytes the waiting items hold.
	Memory   int64
	Interval time.Duration
	// Eager sends every item as soon as it comes.
	Eager  bool
	Logger *slog.Logger
	Counts QueueCounts
	// ExporterUp is the exporter's state, shared by its queues.
	ExporterUp *atomic.Int64
	// Send sends one batch; Close closes the sender once the queue stops.
	Send  func(context.Context, [][]byte) error
	Close func(context.Context) error
	// Disk, when it names a folder, is where the queue keeps what does not
	// fit in memory; what an earlier run left there is sent first.
	Disk QueueDisk
}

// QueueDisk is the folder a queue keeps what does not fit in memory in,
// and the budget of bytes it shares with the exporter's other queue. Spans
// says the queue holds spans, each call's kept together, rather than
// records.
type QueueDisk struct {
	Folder string
	Budget *queuefiles.Budget
	Spans  bool
}

// QueueCounts are the counters of what a queue sent and dropped, and the
// labels that name its exporter and signal on both.
type QueueCounts struct {
	Sent    metric.Int64Counter
	Dropped metric.Int64Counter
	Labels  attribute.Set
}

// NewQueue starts a queue that sends on its own every interval. It fails
// only when the disk folder cannot be opened.
func NewQueue(s QueueSetup) (*Queue, error) {
	q := buildQueue(s)
	if s.Disk.Folder != "" {
		kind := queuefiles.Record
		if s.Disk.Spans {
			kind = queuefiles.Spans
		}
		o, err := newDiskQueue(s.Disk.Folder, s.Disk.Budget, diskQueueSetup{
			kind: kind, logger: s.Logger, name: s.ExporterName, dropped: s.Counts.Dropped, labels: s.Counts.Labels, kick: q.wake,
		})
		if err != nil {
			return nil, err
		}
		q.disk = o
	}
	go q.run()
	if q.disk != nil && !q.disk.empty() {
		q.wake()
	}
	return q, nil
}

// newMemoryQueue starts a queue with no disk folder, which cannot fail.
func newMemoryQueue(s QueueSetup) *Queue {
	q := buildQueue(s)
	go q.run()
	return q
}

// buildQueue is a queue that has not started sending.
func buildQueue(s QueueSetup) *Queue {
	closeSender := s.Close
	if closeSender == nil {
		closeSender = func(context.Context) error { return nil }
	}
	return &Queue{
		export:     s.Send,
		close:      closeSender,
		endpoint:   s.ExporterName,
		signalName: s.ItemName,
		size:       s.Size,
		memory:     s.Memory,
		eager:      s.Eager,
		interval:   s.Interval,
		logger:     s.Logger,
		sent:       s.Counts.Sent,
		dropped:    s.Counts.Dropped,
		labels:     s.Counts.Labels,
		up:         s.ExporterUp,
		// The places past size are kept for the ledger's batch records.
		ring:     make([][]byte, s.Size+batchRecordReserve),
		queuedAt: make([]time.Time, s.Size+batchRecordReserve),
		full:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// wake has the sender send now, not at its next interval.
func (q *Queue) wake() {
	select {
	case q.full <- struct{}{}:
	default:
	}
}

// Add queues items without waiting: all of them, or none when they do not
// all fit, so the spans of one call are never split. It says whether they
// were queued.
func (q *Queue) Add(items ...[]byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	weight := int64(0)
	if q.memory > 0 {
		for _, item := range items {
			weight += int64(len(item))
		}
	}
	fits := len(items) <= q.size-q.count && (q.memory == 0 || q.held+weight <= q.memory)
	if q.disk != nil && q.disk.goesToDisk(fits) {
		if q.disk.add(items) {
			return true
		}
		fits = false
	}
	if !fits {
		q.crowded.count += len(items)
		q.crowded.groups++
		q.dropped.Add(context.Background(), int64(len(items)), dropReason(q.labels, "queue_full"))
		return false
	}
	q.put(items, weight)
	return true
}

// put adds items at the end of the ring. The caller holds the lock and
// has checked they fit.
func (q *Queue) put(items [][]byte, weight int64) {
	end := (q.first + q.count) % len(q.ring)
	copied := copy(q.ring[end:], items)
	copy(q.ring, items[copied:])
	now := time.Now()
	for i := range items {
		q.queuedAt[(end+i)%len(q.queuedAt)] = now
	}
	q.count += len(items)
	q.held += weight
	if q.count >= batchSize || q.eager {
		q.wake()
	}
}

// addBatchRecord queues one of the ledger's batch records behind the records
// before it, without waiting. It may take the places kept for batch
// records and passes the memory limit, so a queue full of calls never
// refuses it; it says whether it found room even so. While the disk is in
// use it goes there, past the disk's limit if need be, as it is small.
func (q *Queue) addBatchRecord(item []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	fits := q.count < len(q.ring)
	if q.disk != nil && q.disk.goesToDisk(fits) {
		q.disk.addBatchRecord(item)
		return true
	}
	if !fits {
		return false
	}
	q.put([][]byte{item}, int64(len(item)))
	return true
}

// peek copies up to one batch of the oldest waiting items into the
// sender's batch, and leaves them in the ring: they keep their places
// until the collector has taken them, or refused them for good. The
// caller holds sendMu, so nothing else takes items meanwhile.
func (q *Queue) peek() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := min(batchSize, q.count)
	q.batch = q.batch[:0]
	for at := q.first; n > 0; at = 0 {
		part := q.ring[at:min(at+n, len(q.ring))]
		q.batch = append(q.batch, part...)
		n -= len(part)
	}
	return q.batch
}

// remove lets go of the n oldest items, sent or refused for good, and
// clears their places in the ring so what they point to can be freed.
// The caller holds sendMu.
func (q *Queue) remove(n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for n > 0 {
		part := q.ring[q.first:min(q.first+n, len(q.ring))]
		if q.memory > 0 {
			for _, item := range part {
				q.held -= int64(len(item))
			}
		}
		clear(part)
		q.first = (q.first + len(part)) % len(q.ring)
		q.count -= len(part)
		n -= len(part)
	}
}

// run sends a batch every interval, or as soon as one fills. While the
// collector is busy or cannot be reached, it tries again after a growing
// wait instead, and the batch stays first in line.
func (q *Queue) run() {
	defer close(q.stopped)
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	wait := time.Duration(0)
	for {
		if wait > 0 {
			retry.Reset(wait)
			select {
			case <-retry.C:
			case <-q.stop:
				retry.Stop()
				return
			}
		} else {
			select {
			case <-ticker.C:
			case <-q.full:
			case <-q.stop:
				return
			}
		}
		if err := q.send(context.Background(), false); busyErr(err) {
			wait = nextWait(wait)
		} else {
			wait = 0
		}
	}
}

// nextWait is how long to wait before trying a busy collector again:
// first about a second, then half as long again each time, at most a few
// seconds.
func nextWait(last time.Duration) time.Duration {
	if last == 0 {
		return retryFirst
	}
	return min(last*3/2, retryMost)
}

// busyErr says the collector was busy or could not be reached: the
// items are kept and tried again.
func busyErr(err error) bool { return errors.Is(err, ErrBusy) }

// send sends what is queued, in batches: all of it when flushing, else
// while a whole batch waits. A batch leaves the queue once the collector
// took it. One the collector refused for good is lost and counted. One
// it was too busy for, or could not be reached for, stays first in line,
// and send returns the error so the caller can try again later
// (Peter, 2026-11-27).
func (q *Queue) send(ctx context.Context, all bool) error {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	defer q.warnDropped(all)
	for {
		batch := q.peek()
		if len(batch) == 0 {
			if q.disk != nil {
				return q.sendDisk(ctx)
			}
			return nil
		}
		n := len(batch)
		err := q.export(ctx, batch)
		// The batch is kept for the next send; let go of what it held.
		clear(batch)
		if busyErr(err) {
			q.up.Store(0)
			return err
		}
		q.remove(n)
		measured(ctx, q.sent, q.dropped, n, q.labels, err, q.up)
		if err != nil {
			q.mu.Lock()
			q.failed.count += n
			q.lastErr = err
			q.mu.Unlock()
			return err
		}
		if !all && q.waitingCount() < batchSize && (q.disk == nil || q.disk.empty()) {
			return nil
		}
	}
}

// sendDisk sends what the disk queue holds, oldest first, until it is
// empty. The sender's place on disk moves only once the collector took a
// batch, or refused it for good; a busy collector leaves it, and the batch
// is read again next time. The caller holds sendMu.
func (q *Queue) sendDisk(ctx context.Context) error {
	for {
		read, at := q.disk.read(batchSize)
		if len(read) == 0 {
			q.disk.taken(at)
			return nil
		}
		q.batch = q.batch[:0]
		for _, item := range read {
			q.batch = append(q.batch, item.Parts...)
		}
		n := len(q.batch)
		err := q.export(ctx, q.batch)
		clear(q.batch)
		if busyErr(err) {
			q.up.Store(0)
			return err
		}
		q.disk.taken(at)
		measured(ctx, q.sent, q.dropped, n, q.labels, err, q.up)
		if err != nil {
			q.mu.Lock()
			q.failed.count += n
			q.lastErr = err
			q.mu.Unlock()
			return err
		}
	}
}

// waitingCount is how many items wait.
func (q *Queue) waitingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

// dropWaiting lets go of every waiting item, counted as lost in a send,
// when the stop time ran out before the collector took them.
func (q *Queue) dropWaiting(err error) {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	// The collector was busy with what waits, so something waits.
	n := q.waitingCount()
	q.remove(n)
	q.dropped.Add(context.Background(), int64(n), dropReason(q.labels, "send_failed"))
	q.mu.Lock()
	q.failed.count += n
	q.lastErr = err
	q.mu.Unlock()
	q.warnDropped(true)
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
func (q *Queue) warnDropped(last bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.crowded.due(last) {
		full := "export queue full"
		if q.disk != nil {
			full = "export queue and its disk queue full"
		}
		q.logger.Warn(full+", dropped the newest "+q.signalName, "endpoint", q.endpoint, "dropped", q.crowded.count, "calls", q.crowded.groups)
		q.crowded.count, q.crowded.groups = 0, 0
	}
	if q.disk != nil {
		q.disk.warn(last)
	}
	if q.failed.due(last) {
		q.logger.Warn("export failed, dropped the "+q.signalName+" it held", "endpoint", q.endpoint, "dropped", q.failed.count, "error", q.lastErr.Error())
		q.failed.count = 0
	}
}

// oldest is when the oldest waiting item was queued, in memory or on
// disk; zero when nothing waits. While the disk is in use, what is in
// memory came before it.
func (q *Queue) oldest() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.count > 0 {
		return q.queuedAt[q.first]
	}
	if q.disk == nil {
		return time.Time{}
	}
	return q.disk.oldestQueued()
}

// fill is how many items wait, and how many the queue holds at most.
func (q *Queue) fill() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count, q.size
}

// ForceFlush sends everything queued.
func (q *Queue) ForceFlush(ctx context.Context) error {
	return q.send(ctx, true)
}

// Shutdown stops the queue, sends what is left and closes the sender.
// A busy collector is tried again until the time ctx allows runs out;
// what it still has not taken then is lost, counted and warned about.
//
// With a disk queue, half the stop time goes to sending; then the writer
// writes what waits, and what is still in memory goes to disk before
// everything there, so the next start sends it first (Peter, 2026-11-27).
func (q *Queue) Shutdown(ctx context.Context) error {
	close(q.stop)
	<-q.stopped
	if q.disk == nil {
		err := q.flushUntil(ctx, true)
		return errors.Join(err, q.close(ctx))
	}
	sending := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		sending, cancel = context.WithDeadline(ctx, time.Now().Add(time.Until(deadline)/2))
		defer cancel()
	}
	err := q.flushUntil(sending, false)
	q.sendMu.Lock()
	var front [][]byte
	var queued []time.Time
	for batch := q.peek(); len(batch) > 0; batch = q.peek() {
		front = append(front, batch...)
		q.mu.Lock()
		for i := range batch {
			queued = append(queued, q.queuedAt[(q.first+i)%len(q.queuedAt)])
		}
		q.mu.Unlock()
		q.remove(len(batch))
	}
	q.sendMu.Unlock()
	return errors.Join(err, q.disk.close(front, queued), q.close(ctx))
}

// flushUntil sends everything queued, trying a busy collector again
// until ctx ends. It returns only a send refused for good.
func (q *Queue) flushUntil(ctx context.Context, dropLeft bool) error {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for wait := time.Duration(0); ; {
		err := q.ForceFlush(ctx)
		if !busyErr(err) {
			return err
		}
		wait = nextWait(wait)
		timer.Reset(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if dropLeft {
				q.dropWaiting(err)
			}
			return nil
		}
	}
}

// newItemQueue is the queue of one kind of written items for one
// exporter. A send writes the waiting items into one request, in a
// buffer and a zipper the queue keeps, since its sends come one at a time.
func newItemQueue(k signal, t *httpTransport, resource []byte, timeout time.Duration, s QueueSetup) (*Queue, error) {
	var body []byte
	z := t.newZipper()
	s.Send = func(ctx context.Context, items [][]byte) error {
		body = appendBatch(body[:0], resource, items)
		return sendWithRetry(ctx, t, k, z, body, timeout)
	}
	// The exporter closes the transport once both queues are done.
	s.Close = nil
	s.Disk.Spans = k == spans
	return NewQueue(s)
}
