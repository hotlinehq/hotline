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

// batchReserve is how many places every exporter's queue keeps for the
// ledger's batch records only, so a queue full of calls never refuses
// one (Peter, 2026-11-27).
const batchReserve = 64

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
// signing (Peter, 2026-11-26). The signer is never kept. Batch never
// blocks. Once signed, each batch record goes into its exporter's queue,
// behind the records before it, in places kept for batch records, so a
// queue full of calls does not refuse it (Peter, 2026-11-27).
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

// batchLine makes one exporter's batch records: it notes the records the
// exporter dropped, fixes each batch record, and once it is signed hands
// it to the exporter's queue, after the records it covers. There it waits,
// is sent, and goes to disk like any record (Peter, 2026-11-27). A batch
// record the queue has no room for even in its reserved places is let go
// and counted; the chain shows the batch is missing.
type batchLine struct {
	batchLineSetup

	mu sync.Mutex
	// signing are the batch records waiting for their signature, by
	// number, and queued counts every one ever made, to number them.
	signing map[uint64]Batch
	queued  uint64
	// drops are the records the exporter dropped, by the batch they
	// belong to, until that batch's record is made.
	drops map[string]*batchDrops
}

// batchLineSetup is what a batch line needs from its exporter.
type batchLineSetup struct {
	name   string
	logger *slog.Logger
	// queue queues a written batch record behind the records before it,
	// and says whether it found room.
	queue  func([]byte) bool
	render func(Batch) []byte
	// resource is what the exporter sends records with, which a batch
	// record's signature covers.
	resource []attribute.KeyValue
	// lost counts a batch record let go.
	lost func()
}

func newBatchLine(setup batchLineSetup) *batchLine {
	return &batchLine{batchLineSetup: setup, signing: map[uint64]Batch{}, drops: map[string]*batchDrops{}}
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

// add makes this exporter's batch record, to be signed, and gives its
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
	l.queued++
	l.signing[l.queued] = mine
	return l.queued, digest
}

// signed puts the signature in batch record number, writes it and queues
// it. A record the signer could not sign is let go and counted, and the
// chain shows the batch is missing.
func (l *batchLine) signed(number uint64, algorithm string, signature []byte, err error) {
	l.mu.Lock()
	record := l.signing[number]
	delete(l.signing, number)
	l.mu.Unlock()
	if err != nil {
		l.lost()
		l.logger.Warn("could not sign a batch record, it is let go", "endpoint", l.name, "error", err.Error())
		return
	}
	record.Attrs = ledger.WithSignature(record.Attrs, algorithm, signature)
	if !l.queue(l.render(record)) {
		l.lost()
	}
}
