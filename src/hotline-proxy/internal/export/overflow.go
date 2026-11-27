package export

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// spillMemory is the most bytes that wait in memory to be written to the
// disk queue. A call never waits for the disk (Peter, 2026-11-27): when
// the disk is slower than the calls, what does not fit here is refused,
// as a full queue refuses.
const spillMemory = 4 << 20

// overflow is the disk queue behind one memory queue. It takes what no
// longer fits in memory, and once it holds anything it takes every new
// item too, until the sender has emptied it, so the order is kept. Its
// own writer writes and syncs; the sender reads (Peter, 2026-11-27).
type overflow struct {
	disk   *diskQueue
	kind   itemKind
	logger *slog.Logger
	name   string
	// dropped counts items lost on disk, with labels naming the
	// exporter and the signal.
	dropped metric.Int64Counter
	labels  attribute.Set
	// kick wakes the sender once items are written.
	kick func()

	mu sync.Mutex
	// spill waits to be written, spillBytes its size; writing counts
	// items the writer took and has not written yet.
	spill      []spilled
	spillBytes int64
	writing    int
	// inUse is set from the first item that goes to disk until the
	// sender has sent everything there.
	inUse bool
	// unreadable counts items lost on disk until the next warning, and
	// started the times the disk started being used.
	unreadable drops
	started    drops

	wake    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

// overflowSetup is what an overflow needs besides its folder.
type overflowSetup struct {
	kind    itemKind
	logger  *slog.Logger
	name    string
	dropped metric.Int64Counter
	labels  attribute.Set
	kick    func()
}

// newOverflow opens the disk queue in dir and starts its writer. Items
// left from an earlier run are sent first.
func newOverflow(dir string, budget *diskBudget, s overflowSetup) (*overflow, error) {
	disk, lost, err := openDiskQueue(dir, budget)
	if err != nil {
		return nil, err
	}
	o := &overflow{
		disk: disk, kind: s.kind, logger: s.logger, name: s.name, dropped: s.dropped, labels: s.labels, kick: s.kick,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), stopped: make(chan struct{}),
	}
	o.inUse = !disk.empty()
	o.lost(lost)
	go o.write()
	return o, nil
}

// takes says whether a new item goes to disk: while the disk is in use,
// or when memory has no room. The caller holds the memory queue's lock,
// so the choice and the memory queue's own stay in step.
func (o *overflow) takes(fitsMemory bool) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inUse || !fitsMemory
}

// add queues items to be written, without waiting: all of them as one
// call's spans, or each record on its own. It says whether they fit in
// the disk queue's budget and in the memory waiting to be written.
func (o *overflow) add(items [][]byte) bool {
	var batch []spilled
	now := time.Now()
	if o.kind == kindSpans {
		batch = []spilled{{kind: kindSpans, payload: joinSpans(items), queued: now}}
	} else {
		for _, item := range items {
			batch = append(batch, spilled{kind: o.kind, payload: item, queued: now})
		}
	}
	size := int64(0)
	for _, s := range batch {
		size += frameSize(s.payload)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spillBytes+size > spillMemory || !o.disk.budget.take(size) {
		return false
	}
	o.spill = append(o.spill, batch...)
	o.spillBytes += size
	o.use()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return true
}

// addBatch queues one of the ledger's batch records to be written. It
// is never refused: it passes the disk's limit and the memory waiting to
// be written, as it is small and there is one per batch.
func (o *overflow) addBatch(item []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	size := frameSize(item)
	o.disk.budget.used.Add(size)
	o.spill = append(o.spill, spilled{kind: kindBatch, payload: item, queued: time.Now()})
	o.spillBytes += size
	o.use()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// use marks the disk in use, and counts each time it starts being used
// for the next warning. The caller holds mu.
func (o *overflow) use() {
	if !o.inUse {
		o.started.count++
	}
	o.inUse = true
}

// write writes what waits, and syncs at most every syncEvery while it
// writes, until the queue stops.
func (o *overflow) write() {
	defer close(o.stopped)
	ticker := time.NewTicker(syncEvery)
	defer ticker.Stop()
	for {
		select {
		case <-o.wake:
			o.writeWaiting()
		case <-ticker.C:
			o.warnIf(o.disk.sync())
		case <-o.stop:
			o.writeWaiting()
			return
		}
	}
}

// writeWaiting writes the items waiting, then wakes the sender.
func (o *overflow) writeWaiting() {
	o.mu.Lock()
	batch := o.spill
	o.spill, o.spillBytes = nil, 0
	o.writing += len(batch)
	o.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	err := o.disk.write(batch)
	o.mu.Lock()
	o.writing -= len(batch)
	o.mu.Unlock()
	o.warnIf(err)
	o.kick()
}

// warnIf warns about a disk queue that could not be written or synced.
// What could not be written is read back as unreadable and counted.
func (o *overflow) warnIf(err error) {
	if err != nil {
		o.logger.Warn("the disk queue could not be written", "endpoint", o.name, "folder", o.disk.dir, "error", err.Error())
	}
}

// read reads up to limit items from disk for the sender, counting those
// it could not read.
func (o *overflow) read(limit int) ([]diskItem, position) {
	items, at, lost := o.disk.read(limit)
	o.lost(lost)
	return items, at
}

// taken moves the sender past what the collector took, and once the disk
// holds nothing more, lets new items go to memory again.
func (o *overflow) taken(at position) {
	o.disk.taken(at)
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.spill) == 0 && o.writing == 0 && o.disk.empty() {
		o.inUse = false
	}
}

// lost counts n items that could not be read from disk.
func (o *overflow) lost(n int) {
	if n == 0 {
		return
	}
	o.dropped.Add(context.Background(), int64(n), dropReason(o.labels, "disk_unreadable"))
	o.mu.Lock()
	o.unreadable.count += n
	o.mu.Unlock()
}

// warn writes how often the disk started being used, as the memory queue
// was full, and how many items on disk could not be read: each at most
// once a minute, and always on the last send.
func (o *overflow) warn(last bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.started.due(last) {
		o.logger.Warn("the export queue was full, new items go to the disk queue until it is empty", "endpoint", o.name, "folder", o.disk.dir, "times", o.started.count)
		o.started.count = 0
	}
	if o.unreadable.due(last) {
		o.logger.Warn("the disk queue held items that could not be read, they were skipped", "endpoint", o.name, "folder", o.disk.dir, "dropped", o.unreadable.count)
		o.unreadable.count = 0
	}
}

// close stops the writer once it has written what waits, puts front, the
// items still in memory and when each was queued, before everything on
// disk, and closes the files.
func (o *overflow) close(front [][]byte, queued []time.Time) error {
	close(o.stop)
	<-o.stopped
	var batch []spilled
	for i, item := range front {
		if o.kind == kindSpans {
			// The memory queue does not keep calls apart, so its spans go
			// back one by one.
			item = joinSpans([][]byte{item})
		}
		batch = append(batch, spilled{kind: o.kind, payload: item, queued: queued[i]})
	}
	err := o.disk.writeFront(batch)
	o.warn(true)
	return errors.Join(err, o.disk.close())
}
