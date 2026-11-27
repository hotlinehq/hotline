package proxy

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/vault"
)

// keyLife is how long the proxy signs with one key before it makes the next
// (Peter, 2026-11-16).
const keyLife = time.Hour

// book is the proxy's evidence ledger (docs/proxy-ledger.md). Every record
// carries its hash and its place in a batch. Each integration has its own
// chain of batches: a batch closes every few seconds, or when it is full,
// and its batch record holds the root of its records' tree and the root of
// the batch before. The ledger signs nothing: it hands each batch, with the
// vault's key, to the exporters, and each signs its own batch record.
type book struct {
	clock     Clock
	exporters *export.Exporters
	instance  string
	// runStarted is when this run started, so two runs of the same
	// instance are told apart (Peter, 2026-11-27).
	runStarted string
	version    string
	config     func() *config.Config
	// hashes makes the record and trace hashes, reusing its writers.
	hashes ledger.Hasher

	mu sync.Mutex
	// vault makes the key each batch is signed with, a new one every hour,
	// and certificate is the newest key's, to tell when a key is new.
	vault       *vault.Vault
	certificate string
	logger      *slog.Logger
	chains      map[string]*chain
	// closedChains are the chains a reload closed with a last batch. A record of
	// a call that started before that reload goes on the same chain.
	closedChains map[string]*chain
	timer        Timer
	every        time.Duration
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

func newBook(clock Clock, exporters *export.Exporters, cfg *config.Config, version string, current func() *config.Config, logger *slog.Logger) *book {
	b := &book{
		clock: clock, exporters: exporters, instance: exporters.ServiceInstanceID(), runStarted: wholeMicros(clock.Now()).UTC().Format(time.RFC3339Nano), version: version, config: current, logger: logger,
		vault: vault.New(exporters.ServiceName(), keyLife, hourlyKeys(cfg.Ledger.Signing), clock.Now()), chains: map[string]*chain{}, closedChains: map[string]*chain{}, every: cfg.Ledger.Every,
	}
	b.timer = clock.AfterFunc(b.every, b.tick)
	return b
}

// record hands a finished call to the exporters with its trace's hash, its
// place in its integration's open batch and its own hash, and notes which
// exporters dropped it. A full batch closes at once.
//
// The chain and the batch come from the config the call started with, not
// the one in use now, so every record in a batch is of the config the
// batch names. A record of another config closes the open batch first
// (Peter, 2026-11-16).
//
// It gives back the record's hash, so a later record can point to it, as
// every call record points to the latest probe of its vendor.
func (b *book) record(ctx context.Context, f finished) string {
	cfg, name := f.cfg, f.chain
	// Every time is cut to whole microseconds, so the hashes cover each
	// time as it is sent. The observed time is read once, here, so the
	// time sent is the time hashed (Peter, 2026-11-25).
	at, observed := wholeMicros(f.at), wholeMicros(b.clock.Now())
	for i := range f.spans {
		f.spans[i].Start, f.spans[i].End = wholeMicros(f.spans[i].Start), wholeMicros(f.spans[i].End)
	}
	// The trace's hash needs nothing from the batch, so it is worked out
	// before the lock that every call's record waits for.
	traceHash := ledger.Text(b.hashes.Trace(f.sc.TraceID().String(), export.LedgerSpans(f.sc, f.spans)))
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.chains[name]
	if ch == nil {
		// A chain a reload ended goes on, for a call that started before.
		ch = b.closedChains[name]
		delete(b.closedChains, name)
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
	call := export.Call{
		Event: f.event, Trace: f.sc, At: at, Observed: observed, Severity: export.Level(attrs), Attrs: attrs, Body: f.body, Spans: f.spans, Content: f.content,
	}
	sum := b.hashes.Record(b.exporters.LedgerRecord(call))
	hash := ledger.Text(sum)
	attrs = append(attrs, attribute.String("hotline.record.hash", hash))
	call.Attrs = attrs
	call.Ledger = export.LedgerEntry{Batch: open.id, Index: len(open.leaves), Hash: hash}
	b.exporters.Call(ctx, call)
	open.leaves = append(open.leaves, sum)
	if len(open.leaves) >= open.maxRecords {
		b.close(ch, false)
	}
	return hash
}

// tick closes every open batch, at the end of each window.
func (b *book) tick() {
	b.mu.Lock()
	defer b.mu.Unlock()
	// A tick that fired as the proxy stopped does nothing.
	if !b.stopped {
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
		// The probes' chain belongs to no integration, and goes on.
		if name != probeChain && cfg.Integration(name).Name != name {
			b.close(ch, true)
			delete(b.chains, name)
			b.closedChains[name] = ch
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
	b.chains, b.closedChains = map[string]*chain{}, map[string]*chain{}
}

// close hands the chain's open batch to the exporters, with the vault's
// key, and each sends its own signed batch record. Only a last batch is
// sent with no records in it.
func (b *book) close(ch *chain, last bool) {
	open := ch.open
	ch.open = nil
	if open == nil {
		// Only a last batch closes with nothing open: it is sent empty.
		ch.lastID = ledger.NewBatchID(b.clock.Now(), ch.lastID)
		open = &openBatch{id: ch.lastID, config: ch.config}
	}
	root := ledger.Text(ledger.Root(open.leaves))
	now := wholeMicros(b.clock.Now())
	attrs := []attribute.KeyValue{
		attribute.String("hotline.batch.format", ledger.BatchFormat),
		attribute.String("hotline.batch.id", open.id),
		attribute.String("hotline.batch.integration", ch.name),
		attribute.String("hotline.batch.instance", b.instance),
		attribute.String("hotline.batch.run_started", b.runStarted),
		attribute.Int64("hotline.batch.sequence", ch.sequence),
		attribute.Int("hotline.batch.records", len(open.leaves)),
		attribute.String("hotline.batch.root", root),
		attribute.String("hotline.batch.version", b.version),
		attribute.String("hotline.batch.config", open.config),
	}
	if ch.previous != "" {
		attrs = append(attrs, attribute.String("hotline.batch.previous", ch.previous))
	}
	if last {
		attrs = append(attrs, attribute.Bool("hotline.batch.last", true))
	}
	batch := export.Batch{ID: open.id, At: now, Observed: now, Attrs: attrs}
	// The signer is asked for once, so every exporter's batch record is
	// signed with the same key.
	signing := b.config().Ledger.Signing
	signer := b.vault.Signer(hourlyKeys(signing), now)
	if signer.Certificate != b.certificate {
		b.certificate = signer.Certificate
		if signing.Authority != nil && now.After(signing.Authority.Certificate.Leaf.NotAfter) {
			// It still signs, as Envoy and HAProxy keep an expired
			// certificate; verify refuses these batches (Peter, 2026-11-26).
			b.logger.Warn("the ledger's signing authority has expired and still signs the new hourly key; verify refuses these batches",
				"cert", signing.CertName, "expired", signing.Authority.Certificate.Leaf.NotAfter.UTC().Format(time.RFC3339))
		}
	}
	b.exporters.Batch(batch, signer)
	ch.previous = root
	ch.sequence++
}

// hourlyKeys is how the vault makes keys under the config's signing
// settings.
func hourlyKeys(s config.Signing) vault.HourlyKeys {
	keys := vault.HourlyKeys{Type: s.HourlyKeyType}
	if s.Authority != nil {
		keys.Authority = s.Authority.Certificate
	}
	return keys
}

// wholeMicros is t cut to a whole microsecond, as the proxy sends every time.
func wholeMicros(t time.Time) time.Time { return t.Truncate(time.Microsecond) }
