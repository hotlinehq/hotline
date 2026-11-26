package export

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"

	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/vault"
)

// waitingBatches is how many batch records wait for one exporter at
// most. Past it the oldest is let go and counted in the proxy's own
// numbers; the chain shows the batch is missing (Peter, 2026-11-25).
const waitingBatches = 64

// batchRetry is how long a batch record that could not be sent waits
// before it is tried again.
const batchRetry = time.Second

// shownDropped is how many dropped records a batch record lists at most,
// by index and hash; past it, only their count.
const shownDropped = 1024

// Batch is a batch record. The ledger hands it over unsigned, with its id,
// times and attributes; each exporter adds its own name and the records it
// dropped, sets the event and the level, and signs it. At and Observed are
// cut to whole microseconds, as the signature covers them.
type Batch struct {
	ID       string
	Event    string
	Severity log.Severity
	At       time.Time
	Observed time.Time
	Attrs    []attribute.KeyValue
}

// signedRecord is a batch record as its signature covers it: every field
// the exporter sends, with the resource it sends it with.
func signedRecord(b Batch, resource []attribute.KeyValue) ledger.Record {
	return ledger.Record{
		Attrs: b.Attrs, At: b.At, Observed: b.Observed,
		SeverityNumber: int(b.Severity), SeverityText: b.Severity.String(), Event: b.Event, Resource: resource,
	}
}

// Batch queues a batch record for every exporter that keeps the ledger.
// Each exporter fixes its own batch record at once; signer signs them all
// afterwards, in one go and apart from the calls, so no call ever waits on
// signing (Peter, 2026-11-26). The signer is never kept. Batch never blocks
// and is never refused for a full queue: the records wait apart from the
// calls' records, after them.
func (s *Exporters) Batch(b Batch, signer vault.Signer) {
	if len(s.batches) == 0 {
		return
	}
	numbers := make([]uint64, len(s.batches))
	digests := make([][]byte, len(s.batches))
	lines := s.batches
	for i, line := range lines {
		var digest [sha256.Size]byte
		numbers[i], digest = line.add(b, signer)
		digests[i] = digest[:]
	}
	s.signing.Add(1)
	go func() {
		defer s.signing.Done()
		signatures, err := signer.Sign(digests)
		for i, line := range lines {
			var signature []byte
			if err == nil {
				signature = signatures[i]
			}
			line.signed(numbers[i], signer.Algorithm, signature, err)
		}
	}()
}

// batchDrops are the records of one batch an exporter dropped: the
// first shownDropped of them, and how many in all.
type batchDrops struct {
	shown []attribute.Value
	count int
}

// list is what the batch record says of the dropped records: each one's
// index and hash, or only their count when there are too many to list. An
// exporter that dropped none lists none, so every batch record says.
func (d *batchDrops) list() attribute.KeyValue {
	if d.count > shownDropped {
		return attribute.Int("hotline.batch.dropped_count", d.count)
	}
	return attribute.Slice("hotline.batch.dropped_records", d.shown...)
}

// batchLine holds the batch records waiting for one exporter and sends
// them once the records queued before them are sent. A batch record that
// cannot be sent waits and is tried again; it is never dropped for a full
// queue.
type batchLine struct {
	batchLineSetup

	mu sync.Mutex
	// waiting are the batch records waiting, oldest first, and queued
	// counts every one ever queued, to number them.
	waiting []waitingBatch
	queued  uint64
	// drops are the records the exporter dropped, by the batch they
	// belong to, until that batch's record is queued.
	drops map[string]*batchDrops

	sendMu  sync.Mutex
	kick    chan struct{}
	stop    chan struct{}
	stopped chan struct{}
}

// waitingBatch is one batch record waiting, numbered in the order it was
// queued. Until it is signed it holds the record without its signature;
// then it is written, so a retry sends the same bytes.
type waitingBatch struct {
	number uint64
	record Batch
	items  [][]byte
}

// batchLineSetup is what a batch line needs from its exporter.
type batchLineSetup struct {
	name   string
	logger *slog.Logger
	// before sends the exporter's waiting records, so a batch record
	// follows its records.
	before func(context.Context) error
	send   func(context.Context, [][]byte) error
	render func(Batch) []byte
	// resource is what the exporter sends records with, which a batch
	// record's signature covers.
	resource []attribute.KeyValue
	// lost counts a batch record let go.
	lost func()
}

func newBatchLine(setup batchLineSetup) *batchLine {
	l := &batchLine{
		batchLineSetup: setup, drops: map[string]*batchDrops{},
		kick: make(chan struct{}, 1), stop: make(chan struct{}), stopped: make(chan struct{}),
	}
	go l.run()
	return l
}

// lostBatch counts a batch record let go for an exporter in
// hotline.exporter.dropped.
func lostBatch(stats sendStats, labels attribute.Set) func() {
	return func() { stats.dropped.Add(context.Background(), 1, dropReason(labels, "queue_full")) }
}

// dropped notes a record the exporter dropped, for its batch record.
func (l *batchLine) dropped(e LedgerEntry) {
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

// add queues this exporter's batch record, to be signed, and gives its
// number and the digest its signature covers. The exporter's drops for
// that batch are final now, as the ledger hands over a call's record and
// closes its batch under the same lock, so the record is made here, once
// (Peter, 2026-11-26).
func (l *batchLine) add(b Batch, signer vault.Signer) (uint64, [sha256.Size]byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d := l.drops[b.ID]
	if d == nil {
		d = &batchDrops{}
	}
	mine := b
	mine.Event, mine.Severity = BatchEvent, log.SeverityInfo
	if d.count > 0 {
		mine.Severity = log.SeverityWarn
	}
	// The exporter is named, so verify can tell one exporter's copy from
	// another's.
	mine.Attrs = append(slices.Clip(b.Attrs), attribute.String("hotline.exporter", l.name), d.list())
	var digest [sha256.Size]byte
	mine.Attrs, digest = ledger.WithCertificate(ledger.BatchFormat, signedRecord(mine, l.resource), signer)
	delete(l.drops, b.ID)
	if len(l.waiting) == waitingBatches {
		clear(l.waiting[:1])
		l.waiting = l.waiting[1:]
		l.lost()
	}
	l.queued++
	l.waiting = append(l.waiting, waitingBatch{number: l.queued, record: mine})
	return l.queued, digest
}

// signed puts the signature in batch record number and writes it, so it
// can be sent. A record the signer could not sign is let go and counted,
// and the chain shows the batch is missing. A record let go as the oldest
// while it was signed stays gone.
func (l *batchLine) signed(number uint64, algorithm string, signature []byte, err error) {
	l.mu.Lock()
	i := slices.IndexFunc(l.waiting, func(w waitingBatch) bool { return w.number == number })
	switch {
	case i < 0:
	case err != nil:
		l.waiting = slices.Delete(l.waiting, i, i+1)
		l.lost()
		l.logger.Warn("could not sign a batch record, it is let go", "endpoint", l.name, "error", err.Error())
	default:
		w := &l.waiting[i]
		w.record.Attrs = ledger.WithSignature(w.record.Attrs, algorithm, signature)
		w.items = [][]byte{l.render(w.record)}
	}
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
	// Only the signed ones go, in order: one still being signed holds back
	// those after it.
	ready := slices.IndexFunc(l.waiting, func(w waitingBatch) bool { return w.items == nil })
	if ready < 0 {
		ready = len(l.waiting)
	}
	waiting := slices.Clone(l.waiting[:ready])
	l.mu.Unlock()
	if len(waiting) == 0 {
		return nil
	}
	var items [][]byte
	for _, w := range waiting {
		items = append(items, w.items...)
	}
	// Records the exporter refused are lost and counted by their own
	// queue; the batch records still go, after them.
	_ = l.before(ctx)
	if err := l.send(ctx, items); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// The ones sent are let go. Some may have been let go already, as the
	// oldest, while these were sent; those queued since still wait.
	last := waiting[len(waiting)-1].number
	sent := 0
	for sent < len(l.waiting) && l.waiting[sent].number <= last {
		sent++
	}
	clear(l.waiting[:sent])
	l.waiting = l.waiting[sent:]
	return nil
}

// shutdown stops the line and sends what waits, within the time ctx
// allows.
func (l *batchLine) shutdown(ctx context.Context) error {
	close(l.stop)
	<-l.stopped
	return l.flush(ctx)
}
