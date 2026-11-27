package export

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
)

// What the tests in export_test may reach of the disk queue: open one on a
// folder, write, read and take items, and break its files under it, to pin
// what it keeps and what it skips.

// QueueFileSize is the most bytes one queue file holds.
const QueueFileSize = queueFileSize

// ErrTestBusy is a send the collector was too busy for.
var ErrTestBusy error = busy{err: errors.New("busy")}

// TestDiskQueue is the queue files of one signal.
type TestDiskQueue struct {
	d    *diskQueue
	last *position
}

// OpenTestDiskQueue opens the queue files in dir, with a budget of limit
// bytes, and says how many items it found it could not read.
func OpenTestDiskQueue(dir string, limit int64) (TestDiskQueue, int, error) {
	d, lost, err := openDiskQueue(dir, &diskBudget{limit: limit})
	return TestDiskQueue{d: d, last: &position{}}, lost, err
}

// Write writes records, each taken from the budget first.
func (t TestDiskQueue) Write(records ...string) error {
	var items []spilled
	for _, r := range records {
		items = append(items, spilled{kind: kindRecord, payload: []byte(r)})
		t.d.budget.take(frameSize([]byte(r)))
	}
	return t.d.write(items)
}

// WriteSpans writes one call's spans as one item.
func (t TestDiskQueue) WriteSpans(spans ...string) error {
	return t.d.write([]spilled{{kind: kindSpans, payload: joinSpans(texts(spans))}})
}

// Read reads up to limit records or spans, and keeps where it ended for
// Taken.
func (t TestDiskQueue) Read(limit int) ([]string, int) {
	items, at, lost := t.d.read(limit)
	*t.last = at
	var read []string
	for _, item := range items {
		for _, b := range item.items {
			read = append(read, string(b))
		}
	}
	return read, lost
}

// Taken moves past what the last Read read.
func (t TestDiskQueue) Taken() { t.d.taken(*t.last) }

// Empty says everything written was read and taken.
func (t TestDiskQueue) Empty() bool { return t.d.empty() }

// Used is the bytes the budget holds.
func (t TestDiskQueue) Used() int64 { return t.d.budget.used.Load() }

// Sync syncs what was written.
func (t TestDiskQueue) Sync() error { return t.d.sync() }

// WriteFront puts records before everything on disk.
func (t TestDiskQueue) WriteFront(records ...string) error {
	var items []spilled
	for _, r := range records {
		items = append(items, spilled{kind: kindRecord, payload: []byte(r)})
	}
	return t.d.writeFront(items)
}

// Close syncs and closes the files.
func (t TestDiskQueue) Close() error { return t.d.close() }

// BreakWriter closes the file being written under the queue, so writes and
// syncs to it fail.
func (t TestDiskQueue) BreakWriter() { _ = t.d.w.Close() }

// BreakPosition closes the position file under the queue.
func (t TestDiskQueue) BreakPosition() { _ = t.d.pos.Close() }

// SplitSpans reads back one call's spans from a payload.
func SplitSpans(payload []byte) []string {
	var spans []string
	for _, s := range splitSpans(payload) {
		spans = append(spans, string(s))
	}
	return spans
}

func texts(items []string) [][]byte {
	b := make([][]byte, len(items))
	for i, item := range items {
		b[i] = []byte(item)
	}
	return b
}

// TestDiskBackedQueue is a queue of records with a disk queue behind it.
type TestDiskBackedQueue struct{ TestQueue }

// NewTestDiskBackedQueue holds size items in memory, the rest in dir up to
// limit bytes, and sends with send only when flushed.
func NewTestDiskBackedQueue(size int, dir string, limit int64, send func(context.Context, []string) error) (TestDiskBackedQueue, error) {
	counter, _ := noop.NewMeterProvider().Meter("test").Int64Counter("test")
	asText := func(ctx context.Context, items [][]byte) error {
		read := make([]string, len(items))
		for i, item := range items {
			read[i] = string(item)
		}
		return send(ctx, read)
	}
	s := queueSetup{
		size: size, interval: time.Hour, logger: slog.New(slog.DiscardHandler),
		sent: counter, dropped: counter, up: &atomic.Int64{},
	}
	q := buildQueue("records", asText, func(context.Context) error { return nil }, s)
	o, err := newOverflow(dir, &diskBudget{limit: limit}, overflowSetup{
		kind: kindRecord, logger: s.logger, dropped: counter, kick: func() {},
	})
	if err != nil {
		return TestDiskBackedQueue{}, err
	}
	q.disk = o
	// The queue sends only when the test flushes it.
	close(q.stopped)
	return TestDiskBackedQueue{TestQueue{q}}, nil
}

// Written says what was queued for disk is written.
func (t TestDiskBackedQueue) Written() bool {
	t.q.disk.mu.Lock()
	defer t.q.disk.mu.Unlock()
	return len(t.q.disk.spill) == 0 && t.q.disk.writing == 0
}

// OnDisk says the disk queue is in use.
func (t TestDiskBackedQueue) OnDisk() bool { return t.q.disk.takes(true) }

// BreakDiskWriter closes the file the disk queue writes, so its writer
// fails.
func (t TestDiskBackedQueue) BreakDiskWriter() { _ = t.q.disk.disk.w.Close() }
