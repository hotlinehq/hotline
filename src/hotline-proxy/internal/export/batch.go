package export

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// waitingBatches is how many batch records wait for one destination at
// most. Past it the oldest is let go, and the next one sent says how many
// were lost (Peter, 2026-11-16).
const waitingBatches = 64

// batchRetry is how long a batch record that could not be sent waits
// before it is tried again.
const batchRetry = time.Second

// shownDropped is how many dropped records a batch record lists at most,
// by index and hash; past it, only their count.
const shownDropped = 1024

// Batch is one ledger batch record: its id and its signed attributes, the
// same for every destination. Each destination adds the records of the
// batch it dropped on its own.
type Batch struct {
	ID    string
	At    time.Time
	Attrs []attribute.KeyValue
}

// Batch queues a batch record for every destination that keeps the
// ledger. It never blocks and is never refused for a full queue: it waits
// apart from the records, after them.
func (s *Sink) Batch(b Batch) {
	for _, line := range s.batches {
		line.add(b)
	}
}

// batchDrops are the records of one batch a destination dropped: the
// first shownDropped of them, and how many in all.
type batchDrops struct {
	shown []attribute.Value
	count int
}

// own is what the destination adds to the batch's record: the records it
// dropped, or only their count when there are too many to list.
func (d *batchDrops) own() []attribute.KeyValue {
	switch {
	case d == nil || d.count == 0:
		return nil
	case d.count > shownDropped:
		return []attribute.KeyValue{attribute.Int("hotline.batch.dropped_count", d.count)}
	}
	return []attribute.KeyValue{attribute.Slice("hotline.batch.dropped", d.shown...)}
}

// batchLine holds the batch records waiting for one destination and sends
// them once the records queued before them are sent. A batch record that
// cannot be sent waits and is tried again; it is never dropped for a full
// queue.
type batchLine struct {
	name   string
	logger *slog.Logger
	// before sends the destination's waiting records, so a batch record
	// follows its records.
	before func(context.Context) error
	send   func(context.Context, [][]byte) error
	render func(Batch, []attribute.KeyValue) []byte

	mu      sync.Mutex
	waiting []waitingBatch
	// drops are the records the destination dropped, by the batch they
	// belong to, until that batch's record is queued.
	drops map[string]*batchDrops
	// lost counts batch records let go since the last one sent.
	lost int64

	sendMu  sync.Mutex
	kick    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

type waitingBatch struct {
	b   Batch
	own []attribute.KeyValue
}

func newBatchLine(name string, logger *slog.Logger, before func(context.Context) error, send func(context.Context, [][]byte) error, render func(Batch, []attribute.KeyValue) []byte) *batchLine {
	l := &batchLine{
		name: name, logger: logger, before: before, send: send, render: render, drops: map[string]*batchDrops{},
		kick: make(chan struct{}, 1), stop: make(chan struct{}), stopped: make(chan struct{}),
	}
	go l.run()
	return l
}

// dropped notes a record the destination dropped, for its batch's record.
func (l *batchLine) dropped(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d := l.drops[e.Batch]
	if d == nil {
		d = &batchDrops{}
		l.drops[e.Batch] = d
	}
	d.count++
	if len(d.shown) < shownDropped {
		d.shown = append(d.shown, attribute.MapValue(attribute.Int("index", e.Index), attribute.String("hash", e.Hash)))
	}
}

func (l *batchLine) add(b Batch) {
	l.mu.Lock()
	own := l.drops[b.ID].own()
	delete(l.drops, b.ID)
	if len(l.waiting) == waitingBatches {
		clear(l.waiting[:1])
		l.waiting = l.waiting[1:]
		l.lost++
	}
	l.waiting = append(l.waiting, waitingBatch{b: b, own: own})
	l.mu.Unlock()
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func (l *batchLine) run() {
	defer close(l.stopped)
	retry := time.NewTimer(batchRetry)
	defer retry.Stop()
	for {
		select {
		case <-l.kick:
		case <-retry.C:
		case <-l.stop:
			return
		}
		if err := l.flush(context.Background()); err != nil {
			l.logger.Warn("could not send the ledger's batch records, they wait", "endpoint", l.name, "error", err.Error())
		}
		retry.Reset(batchRetry)
	}
}

// flush sends the records waiting before the batch records, then the
// batch records. Those sent are let go; on a failure they all wait.
func (l *batchLine) flush(ctx context.Context) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	l.mu.Lock()
	waiting := slices.Clone(l.waiting)
	lost := l.lost
	l.mu.Unlock()
	if len(waiting) == 0 {
		return nil
	}
	items := make([][]byte, len(waiting))
	for i, w := range waiting {
		attrs := append(slices.Clip(w.b.Attrs), w.own...)
		if i == 0 && lost > 0 {
			attrs = append(attrs, attribute.Int64("hotline.batch.lost_batches", lost))
		}
		items[i] = l.render(w.b, attrs)
	}
	// Records the destination refused are lost and counted by their own
	// queue; the batch records still go, after them.
	_ = l.before(ctx)
	if err := l.send(ctx, items); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Batch records let go while these were sent were the oldest: these,
	// already sent, so not lost.
	since := l.lost - lost
	gone := min(int64(len(waiting)), since)
	sent := len(waiting) - int(gone)
	clear(l.waiting[:sent])
	l.waiting = l.waiting[sent:]
	l.lost = since - gone
	return nil
}

// shutdown stops the line and sends what waits, within the time ctx
// allows.
func (l *batchLine) shutdown(ctx context.Context) error {
	close(l.stop)
	<-l.stopped
	return l.flush(ctx)
}
