package proxy

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
)

// keyLife is how long the proxy signs with one key before it makes the next
// (Peter, 2026-11-16).
const keyLife = time.Hour

// book is the proxy's evidence ledger (docs/proxy-ledger.md). Every record
// carries its hash and its place in a batch. Each integration has its own
// chain of batches: a batch closes every few seconds, or when it is full,
// and its batch record holds the root of its records' tree, the root of
// the batch before, and a signature with a key the proxy makes in memory.
type book struct {
	clock    Clock
	sink     *export.Sink
	instance string
	// run, when the config names the instance, is this run's own random id.
	run     string
	version string
	config  func() *config.Config
	// hashes makes the record and trace hashes, reusing its writers.
	hashes ledger.Hasher

	mu     sync.Mutex
	key    ledger.Key
	keyAt  time.Time
	chains map[string]*chain
	// ended are the chains a reload closed with a last batch. A record of
	// a call that started before that reload goes on the same chain.
	ended map[string]*chain
	timer Timer
	every time.Duration
	// stopped is set once the last batches went out.
	stopped bool
}

// chain is one integration's batches.
type chain struct {
	name     string
	sequence int64
	// previous is the root of the last batch sent, empty before the first.
	previous string
	// lastID is the id of the newest batch, so the next sorts after it.
	lastID string
	// config is the config of the newest batch, which an empty last batch
	// names too: not the config in use now, which may no longer have this
	// integration (Peter, 2026-11-17, from the decomplect review).
	config string
	open   *openBatch
}

// openBatch is a batch still taking records, all of calls that started
// with the same config.
type openBatch struct {
	id     string
	config string
	// maxRecords is that config's ledger.maxRecords.
	maxRecords int
	leaves     [][sha256.Size]byte
}

func newBook(clock Clock, sink *export.Sink, cfg *config.Config, instanceID, version string, current func() *config.Config) *book {
	b := &book{
		clock: clock, sink: sink, instance: instanceID, version: version, config: current,
		key: ledger.NewKey(), keyAt: clock.Now(), chains: map[string]*chain{}, ended: map[string]*chain{}, every: cfg.Ledger.Every,
	}
	if cfg.Ledger.Instance != "" {
		// A named instance keeps the random id next to it, so two runs of
		// the same name are told apart.
		b.instance, b.run = cfg.Ledger.Instance, instanceID
	}
	b.timer = clock.AfterFunc(b.every, b.tick)
	return b
}

// record hands a finished call to the exporters with its trace's hash, its
// place in its integration's open batch and its own hash, and notes which
// destinations dropped it. A full batch closes at once.
//
// The chain and the batch come from the config the call started with, not
// the one in use now, so every record in a batch is of the config the
// batch names. A record of another config closes the open batch first
// (Peter, 2026-11-16).
func (b *book) record(ctx context.Context, f finished) {
	cfg, name := f.cfg, f.chain
	traceID, spanID := f.sc.TraceID().String(), f.sc.SpanID().String()
	// The trace's hash needs nothing from the batch, so it is worked out
	// before the lock that every call's record waits for.
	traceHash := ledger.Text(b.hashes.Trace(traceID, ledgerSpans(f.spans)))
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.chains[name]
	if ch == nil {
		// A chain a reload ended goes on, for a call that started before.
		ch = b.ended[name]
		delete(b.ended, name)
		if ch == nil {
			ch = &chain{name: name}
		}
		b.chains[name] = ch
	}
	if ch.open != nil && ch.open.config != cfg.Digest {
		b.close(ch, false)
	}
	if ch.open == nil {
		ch.lastID, ch.config = ledger.NewBatchID(b.clock.Now(), ch.lastID), cfg.Digest
		ch.open = &openBatch{
			id: ch.lastID, config: cfg.Digest, maxRecords: cfg.Ledger.MaxRecords,
		}
	}
	open := ch.open
	// f.attrs was made with room for these, so they need no new array.
	attrs := append(f.attrs, //nolint:gocritic // the caller's f.attrs keeps its length, so it never sees these
		attribute.String("hotline.trace.hash", traceHash),
		attribute.String("hotline.batch.id", open.id),
		attribute.Int("hotline.batch.index", len(open.leaves)),
	)
	sum := b.hashes.Record(ledger.Record{Attrs: attrs, At: f.at, TraceID: traceID, SpanID: spanID})
	hash := ledger.Text(sum)
	attrs = append(attrs, attribute.String("hotline.record.hash", hash))
	b.sink.Call(ctx, export.Call{
		Trace: f.sc, At: f.at, Attrs: attrs, Spans: f.spans, Content: f.content,
		Ledger: export.Entry{Batch: open.id, Index: len(open.leaves), Hash: hash},
	})
	open.leaves = append(open.leaves, sum)
	if len(open.leaves) >= open.maxRecords {
		b.close(ch, false)
	}
}

// tick closes every open batch, at the end of each window, and makes a new
// key once the old one has signed for an hour.
func (b *book) tick() {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A tick that fired as the proxy stopped does nothing.
	if !b.stopped {
		if now := b.clock.Now(); now.Sub(b.keyAt) >= keyLife {
			b.key, b.keyAt = ledger.NewKey(), now
		}
		for _, ch := range b.chains {
			if ch.open != nil {
				b.close(ch, false)
			}
		}
		b.every = b.config().Ledger.Every
		b.timer = b.clock.AfterFunc(b.every, b.tick)
	}
}

// reloaded ends the chains of integrations the new config no longer has,
// each with a last batch. An ended chain is kept, so a record of a call
// that started before the reload still goes on it, after the last batch.
func (b *book) reloaded(cfg *config.Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Once stopped, the book has no chains left to end.
	for name, ch := range b.chains {
		if cfg.Integration(name).Name != name {
			b.close(ch, true)
			delete(b.chains, name)
			b.ended[name] = ch
		}
	}
}

// stop closes every chain with a last batch, sent even with no records in
// it, so a clean stop shows (Peter, 2026-11-16).
func (b *book) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	b.timer.Stop()
	for _, ch := range b.chains {
		b.close(ch, true)
	}
	// A record that comes late, after a stop that ran out of time, starts
	// a chain that no batch closes.
	b.chains, b.ended = map[string]*chain{}, map[string]*chain{}
}

// close signs the chain's open batch and sends its batch record. Only a
// last batch is sent with no records in it.
func (b *book) close(ch *chain, last bool) {
	open := ch.open
	ch.open = nil
	if open == nil {
		// Only a last batch closes with nothing open: it is sent empty.
		ch.lastID = ledger.NewBatchID(b.clock.Now(), ch.lastID)
		open = &openBatch{id: ch.lastID, config: ch.config}
	}
	root := ledger.Text(ledger.Root(open.leaves))
	attrs := []attribute.KeyValue{
		attribute.String("hotline.batch.format", ledger.BatchFormat),
		attribute.String("hotline.batch.id", open.id),
		attribute.String("hotline.batch.integration", ch.name),
		attribute.String("hotline.batch.instance", b.instance),
		attribute.Int64("hotline.batch.sequence", ch.sequence),
		attribute.Int("hotline.batch.records", len(open.leaves)),
		attribute.String("hotline.batch.root", root),
		attribute.String("hotline.batch.key", b.key.Public),
		attribute.String("hotline.batch.version", b.version),
		attribute.String("hotline.batch.config", open.config),
	}
	if b.run != "" {
		attrs = append(attrs, attribute.String("hotline.batch.run", b.run))
	}
	if ch.previous != "" {
		attrs = append(attrs, attribute.String("hotline.batch.previous", ch.previous))
	}
	if last {
		attrs = append(attrs, attribute.Bool("hotline.batch.last", true))
	}
	attrs = append(attrs, b.key.Sign(attrs))
	b.sink.Batch(export.Batch{ID: open.id, At: b.clock.Now(), Attrs: attrs})
	ch.previous = root
	ch.sequence++
}

// ledgerSpans are a trace's spans as the trace's hash covers them, with the
// kind and status as the file exporter writes them.
func ledgerSpans(spans []export.Span) []ledger.Span {
	out := make([]ledger.Span, len(spans))
	for i, s := range spans {
		parent := ""
		if s.Parent.IsValid() {
			parent = s.Parent.String()
		}
		out[i] = ledger.Span{
			ID: s.ID.String(), Parent: parent, Name: s.Name, Kind: spanKind(s.Kind),
			Start: s.Start, End: s.End, Status: s.Status.String(), Attrs: s.Attrs,
		}
	}
	return out
}

func spanKind(k trace.SpanKind) string { return k.String() }
