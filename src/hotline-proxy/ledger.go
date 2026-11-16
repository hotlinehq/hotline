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

// shownDropped is how many dropped records a batch record lists at most,
// for one destination. Past it only their count is sent.
const shownDropped = 1024

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

	mu     sync.Mutex
	key    ledger.Key
	keyAt  time.Time
	chains map[string]*chain
	timer  Timer
	every  time.Duration
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
	open   *openBatch
}

// openBatch is a batch still taking records.
type openBatch struct {
	id     string
	config string
	leaves [][sha256.Size]byte
	// dropped are, for each destination that keeps the ledger, the records
	// it did not take, up to shownDropped, and how many in all.
	dropped [][]attribute.Value
	count   []int
}

func newBook(clock Clock, sink *export.Sink, cfg *config.Config, instanceID, version string, current func() *config.Config) *book {
	b := &book{
		clock: clock, sink: sink, instance: instanceID, version: version, config: current,
		key: ledger.NewKey(), keyAt: clock.Now(), chains: map[string]*chain{}, every: cfg.Ledger.Every,
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
func (b *book) record(ctx context.Context, f finished) {
	cfg := b.config()
	name := cfg.Chain(integrationOf(f.attrs))
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.chains[name]
	if ch == nil {
		ch = &chain{name: name}
		b.chains[name] = ch
	}
	if ch.open == nil {
		ch.lastID = ledger.NewBatchID(b.clock.Now(), ch.lastID)
		ch.open = &openBatch{
			id: ch.lastID, config: cfg.Digest,
			dropped: make([][]attribute.Value, b.sink.Ledgers()), count: make([]int, b.sink.Ledgers()),
		}
	}
	open := ch.open
	traceID, spanID := f.sc.TraceID().String(), f.sc.SpanID().String()
	attrs := append(f.attrs,
		attribute.String("hotline.trace.hash", ledger.Text(ledger.TraceHash(traceID, ledgerSpans(f.spans)))),
		attribute.String("hotline.batch.id", open.id),
		attribute.Int("hotline.batch.index", len(open.leaves)),
	)
	sum := ledger.RecordHash(ledger.Record{Attrs: attrs, At: f.at, TraceID: traceID, SpanID: spanID})
	hash := ledger.Text(sum)
	attrs = append(attrs, attribute.String("hotline.record.hash", hash))
	taken := b.sink.Call(ctx, export.Call{Trace: f.sc, At: f.at, Attrs: attrs, Spans: f.spans, Content: f.content})
	index := len(open.leaves)
	open.leaves = append(open.leaves, sum)
	for i, ok := range taken {
		if ok {
			continue
		}
		open.count[i]++
		if len(open.dropped[i]) < shownDropped {
			open.dropped[i] = append(open.dropped[i], attribute.MapValue(
				attribute.Int("index", index), attribute.String("hash", hash)))
		}
	}
	if len(open.leaves) >= cfg.Ledger.MaxRecords {
		b.close(ch, false)
	}
}

// tick closes every open batch, at the end of each window, and makes a new
// key once the old one has signed for an hour.
func (b *book) tick() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
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

// reloaded ends the chains of integrations the new config no longer has,
// each with a last batch.
func (b *book) reloaded(cfg *config.Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	for name, ch := range b.chains {
		if cfg.Chain(name) != name {
			b.close(ch, true)
			delete(b.chains, name)
		}
	}
}

// stop closes every chain with a last batch, sent even with no records in
// it, so a clean stop shows (Peter, 2026-11-16).
func (b *book) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.stopped = true
	b.timer.Stop()
	for _, ch := range b.chains {
		b.close(ch, true)
	}
}

// close signs the chain's open batch and sends its batch record. Only a
// last batch is sent with no records in it.
func (b *book) close(ch *chain, last bool) {
	open := ch.open
	ch.open = nil
	if open == nil {
		if !last {
			return
		}
		ch.lastID = ledger.NewBatchID(b.clock.Now(), ch.lastID)
		open = &openBatch{id: ch.lastID, config: b.config().Digest}
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
	own := make([][]attribute.KeyValue, b.sink.Ledgers())
	for i := range own {
		switch {
		case i >= len(open.count) || open.count[i] == 0:
		case open.count[i] > shownDropped:
			own[i] = []attribute.KeyValue{attribute.Int("hotline.batch.dropped_count", open.count[i])}
		default:
			own[i] = []attribute.KeyValue{attribute.Slice("hotline.batch.dropped", open.dropped[i]...)}
		}
	}
	b.sink.Batch(export.Batch{At: b.clock.Now(), Attrs: attrs, Own: own})
	ch.previous = root
	ch.sequence++
}

// integrationOf is the integration a record names.
func integrationOf(attrs []attribute.KeyValue) string {
	for _, kv := range attrs {
		if kv.Key == "hotline.integration.id" {
			return kv.Value.AsString()
		}
	}
	return ""
}

// ledgerSpans are a trace's spans as the trace's hash covers them, with the
// kind and status as the file exporter writes them.
func ledgerSpans(spans []export.Span) []ledger.Span {
	out := make([]ledger.Span, len(spans))
	for i, s := range spans {
		status := "unset"
		if s.OK {
			status = "ok"
		}
		if s.Failed {
			status = "error"
		}
		parent := ""
		if s.Parent.IsValid() {
			parent = s.Parent.String()
		}
		out[i] = ledger.Span{
			ID: s.ID.String(), Parent: parent, Name: s.Name, Kind: spanKind(s.Kind),
			Start: s.Start, End: s.End, Status: status, Attrs: s.Attrs,
		}
	}
	return out
}

func spanKind(k trace.SpanKind) string { return k.String() }
