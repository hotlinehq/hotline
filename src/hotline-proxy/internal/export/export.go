// Package export sends records and their traces over OTLP, and writes
// them to files and the console.
// Every exporter gets its own queue, so one that is down never holds up
// the others or the calls.
package export

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/ledger"
)

// Timing says how long one send to an exporter may take, retries
// included. How often each exporter is sent to is in its config.
type Timing struct {
	Timeout time.Duration
}

// Service describes the proxy on every signal it sends.
type Service struct {
	Version    string
	InstanceID string
}

// Exporters takes finished calls and sends them to every exporter.
type Exporters struct {
	exporters []exporter
	// files are the folders every call is written to, one line per call.
	files []fileExporter
	// console, when set, prints every call to standard output.
	console *consoleExporter
	// batches hold the ledger's batch records waiting for each
	// exporter that takes records, the OTLP ones first, then the files.
	batches []*batchLine
	// signing counts the batches being signed, so a shutdown waits for
	// them.
	signing sync.WaitGroup
	// resource is the resource every exporter sends, the same for all of
	// them, as every record's hash covers it.
	resource []attribute.KeyValue
	// lines holds buffers to write a call line in.
	lines sync.Pool
	// scratch holds buffers to write a record or a call's spans in,
	// before they are copied to their own exact size.
	scratch sync.Pool
}

// exporter is the pipeline of one otlp exporter.
type exporter struct {
	// queue holds the written records and spans the written spans, both
	// sent with transport.
	queue     *queue
	spans     *queue
	transport *httpTransport
	// up is 1 while sends work, 0 from a failed send until one works, and
	// place names the exporter in the proxy's own numbers.
	up    *atomic.Int64
	place attribute.Set
	// batches sends the ledger's batch records to the exporter, and
	// notes the records its queue dropped.
	batches *batchLine
	// lock holds the exporter's queueFolder, when it has one.
	lock *os.File
}

// fileExporter is one file exporter's queue of written lines and its
// writer.
type fileExporter struct {
	queue   *queue
	writer  *fileWriter
	up      *atomic.Int64
	place   attribute.Set
	batches *batchLine
}

// Call is one finished call, or a WebSocket session: its record and its
// spans.
type Call struct {
	// Event is the record's event name: empty for a call, which is
	// hotline.request, or SessionEvent for a WebSocket session.
	Event string
	Trace trace.SpanContext
	// At is the record's time, and Observed when the record was written:
	// both cut to whole microseconds, as the record's hash covers them.
	At, Observed time.Time
	// Severity is the record's level, worked out once by Level, before
	// the hash, so the level hashed is the level sent.
	Severity log.Severity
	// Attrs is the record every exporter gets.
	Attrs []attribute.KeyValue
	// Body is the record's text, when it has one: a probe's verdict in a
	// sentence. Calls have none.
	Body  string
	Spans []Span
	// Content, under content: full, is what file exporters get besides
	// the record: every header and the bodies, under hotline.content.*
	// names.
	Content []attribute.KeyValue
	// Ledger is the record's place in its ledger batch. An exporter
	// that drops the record lists it there in the batch's record.
	Ledger LedgerEntry
}

// LedgerEntry is a record's place in its ledger batch: the batch, its index in
// it, and its hash.
type LedgerEntry struct {
	Batch string
	Index int
	Hash  string
}

// fileInterval is how long a call line waits at most before it is
// written: short, so a line shows up at once, yet long enough to write
// many lines in one go under load (Peter, 2026-11-11).
const fileInterval = 100 * time.Millisecond

// fileQueueSize is how many calls wait for a file exporter at most.
const fileQueueSize = 10_000

// Setup is what New needs: the exporters from the config, where the
// console writes, the roots TLS trusts, and how the proxy describes and
// times itself.
type Setup struct {
	// OTLP are the otlp exporters, Files the file exporters.
	OTLP  []config.Exporter
	Files []config.FileExporter
	// Console is nil when the config has no console exporter.
	Console *config.ConsoleExporter
	Out     Console
	Roots   *x509.CertPool
	Service Service
	// Labels are the resource labels of the records section's transform
	// steps, on top of the proxy's own (Peter, 2026-11-25).
	Labels map[string]string
	Timing Timing
	Logger *slog.Logger
	Meter  metric.Meter
}

// New starts one queue and sender per exporter. Nothing is sent to a
// exporter before the first record, so an exporter that is down does
// not stop the proxy from starting. TLS to an exporter trusts roots and
// the exporter's own CAs.
//
// Only the config says how to send: every option is set here, so the
// library never falls back to the OTEL_EXPORTER_OTLP_* environment
// variables.
//
// Each file exporter takes its folder's lock, so New fails with
// ErrFolderTaken when another proxy writes to the same folder.
func New(setup Setup) (*Exporters, error) {
	exporters, files, console, out := setup.OTLP, setup.Files, setup.Console, setup.Out
	roots, service, timing, logger, meter := setup.Roots, setup.Service, setup.Timing, setup.Logger, setup.Meter
	s := &Exporters{}
	s.scratch.New = func() any {
		b := make([]byte, 0, 4096)
		return &b
	}
	s.lines.New = func() any { return &bytes.Buffer{} }
	stats := newSendStats(meter)
	s.resource = describe(service, setup.Labels).Attributes()
	written := resourceOf(s.resource)
	for _, e := range files {
		writer, err := openFileWriter(e)
		if err != nil {
			s.closeFiles()
			return nil, err
		}
		up := &atomic.Int64{}
		up.Store(1)
		label := attribute.String("hotline.exporter", e.Path)
		setup := queueSetup{
			endpoint: e.Path, size: fileQueueSize, memory: e.QueueMemory, interval: fileInterval, logger: logger, up: up,
			sent: stats.sent, dropped: stats.dropped, labels: attribute.NewSet(label, attribute.String("hotline.signal", "record")),
		}
		write := func(_ context.Context, lines [][]byte) error { return writer.write(lines) }
		s.files = append(s.files, fileExporter{
			queue:  newQueue("records", write, func(context.Context) error { return writer.close() }, setup),
			writer: writer,
			up:     up,
			place:  attribute.NewSet(label),
		})
	}
	for _, e := range exporters {
		t := newHTTPTransport(e, tlsFor(e, roots), service.Version)
		up := &atomic.Int64{}
		up.Store(1)
		setup := queueSetup{endpoint: e.Endpoint, size: e.QueueSize, logger: logger, up: up}
		setup.sent, setup.dropped = stats.sent, stats.dropped
		logSetup := setup
		logSetup.labels = signalLabels(e, "record")
		logSetup.interval = e.LogInterval
		spanSetup := setup
		spanSetup.labels = signalLabels(e, "span")
		spanSetup.interval = e.TraceInterval
		d := exporter{transport: t, up: up, place: attribute.NewSet(exporterLabel(e))}
		recordDisk, spanDisk, err := d.openDisk(e)
		if err == nil {
			d.queue, err = newItemQueue("records", records, t, written, timing.Timeout, logSetup, recordDisk)
		}
		if err == nil {
			d.spans, err = newItemQueue("spans", spans, t, written, timing.Timeout, spanSetup, spanDisk)
		}
		if err != nil {
			s.exporters = append(s.exporters, d)
			s.closeAll()
			return nil, fmt.Errorf("exporter %s: the disk queue in %s: %w", e.Endpoint, e.QueueFolder, err)
		}
		// The batch line sends records too, at the same time as the record
		// queue, so it has its own zipper.
		batchZip := t.newZipper()
		d.batches = newBatchLine(batchLineSetup{
			name: e.Endpoint, logger: logger, before: d.queue.ForceFlush, resource: s.resource,
			send: func(ctx context.Context, items [][]byte) error {
				body := appendBatch(nil, written, items)
				return sendWithRetry(ctx, t, records, batchZip, body, timing.Timeout)
			},
			render: func(b Batch) []byte {
				return appendRecord(nil, b.Event, b.At, b.Observed, b.Severity, trace.SpanContext{}, "", b.Attrs)
			},
			lost: lostBatch(stats, attribute.NewSet(exporterLabel(e), attribute.String("hotline.signal", "batch"))),
		})
		s.exporters = append(s.exporters, d)
		s.batches = append(s.batches, d.batches)
	}
	for i := range s.files {
		f := &s.files[i]
		f.batches = newBatchLine(batchLineSetup{
			name: f.writer.e.Path, logger: logger, before: f.queue.ForceFlush, resource: s.resource,
			send: func(_ context.Context, items [][]byte) error { return f.writer.write(items) },
			render: func(b Batch) []byte {
				buf := &bytes.Buffer{}
				appendBatchLine(buf, s.resource, b)
				return buf.Bytes()
			},
			lost: lostBatch(stats, attribute.NewSet(attribute.String("hotline.exporter", f.writer.e.Path), attribute.String("hotline.signal", "batch"))),
		})
		s.batches = append(s.batches, f.batches)
	}
	if console != nil {
		up := &atomic.Int64{}
		up.Store(1)
		label := attribute.String("hotline.exporter", "console")
		s.console = newConsole(console, out, queueSetup{
			endpoint: "console", size: consoleQueueSize, interval: fileInterval, eager: true, logger: logger, up: up,
			sent: stats.sent, dropped: stats.dropped, labels: attribute.NewSet(label, attribute.String("hotline.signal", "record")),
		})
	}
	observeExporters(meter, s.exporters, s.local())
	return s, nil
}

// local are the exporters on this machine: the files and the console.
func (s *Exporters) local() []fileExporter {
	if s.console == nil {
		return s.files
	}
	return append(slices.Clip(s.files), s.console.fileExporter)
}

// closeFiles lets go of the folders taken so far, when New fails.
func (s *Exporters) closeFiles() {
	for _, f := range s.files {
		_ = f.queue.Shutdown(context.Background())
	}
}

// closeAll stops every queue started so far and lets go of their folders,
// when New fails.
func (s *Exporters) closeAll() {
	ctx, cancel := context.WithCancel(context.Background())
	// Nothing has been sent yet; what waits stays on disk.
	cancel()
	for _, b := range s.batches {
		_ = b.shutdown(ctx)
	}
	for _, d := range s.exporters {
		for _, q := range []*queue{d.queue, d.spans} {
			if q != nil {
				_ = q.Shutdown(ctx)
			}
		}
		if d.lock != nil {
			_ = d.lock.Close()
		}
		d.transport.close()
	}
	s.closeFiles()
}

// openDisk takes an exporter's queueFolder, with its lock, and gives
// where its records and its spans go when memory is full: no folder when
// the config names none.
func (d *exporter) openDisk(e config.Exporter) (diskSetup, diskSetup, error) {
	if e.QueueFolder == "" {
		return diskSetup{}, diskSetup{}, nil
	}
	lock, err := os.OpenFile(filepath.Join(e.QueueFolder, queueLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return diskSetup{}, diskSetup{}, err
	}
	if err := lockExclusive(lock); err != nil {
		_ = lock.Close()
		return diskSetup{}, diskSetup{}, ErrFolderTaken
	}
	d.lock = lock
	budget := &diskBudget{limit: e.QueueDisk}
	return diskSetup{folder: filepath.Join(e.QueueFolder, "records"), budget: budget},
		diskSetup{folder: filepath.Join(e.QueueFolder, "spans"), budget: budget}, nil
}

// ServiceName is the service.name every exporter sends records with: the
// proxy's own, or the one the config's labels give it. A batch key's
// certificate names it.
func (s *Exporters) ServiceName() string {
	// The resource always holds service.name.
	labels := attribute.NewSet(s.resource...)
	name, _ := labels.Value(semconv.ServiceNameKey)
	return name.AsString()
}

// ServiceInstanceID is the resource's service.instance.id: the label the
// config gives it, or the proxy's own id for this run.
func (s *Exporters) ServiceInstanceID() string {
	// The resource always holds service.instance.id.
	labels := attribute.NewSet(s.resource...)
	id, _ := labels.Value(semconv.ServiceInstanceIDKey)
	return id.AsString()
}

// describe is the resource the proxy's signals go with: its own labels,
// with the config's labels on top. The config has refused service.version.
func describe(service Service, labels map[string]string) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName("hotline-proxy"),
		semconv.ServiceVersion(service.Version),
		semconv.ServiceInstanceID(service.InstanceID),
	}
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		attrs = append(attrs, attribute.String(name, labels[name]))
	}
	// A later label with the same name wins.
	return resource.NewSchemaless(attrs...)
}

// A busy exporter is tried again, first after about a second, then at
// most every few seconds, until the send's time is up.
const (
	retryFirst = time.Second
	retryMost  = 5 * time.Second
)

// tlsFor is the TLS setup for an exporter, nil for a plain one.
func tlsFor(e config.Exporter, roots *x509.CertPool) *tls.Config {
	if e.URL.Scheme != "https" {
		return nil
	}
	return exporterTLS(e, roots)
}

// Call queues one call for every exporter: its record and its spans for
// each OTLP exporter, one line for each file, and its
// lines on the console. It never blocks: a full queue refuses what is new,
// and an exporter that keeps the ledger notes the record it dropped, to
// list it in the record of the record's batch.
func (s *Exporters) Call(_ context.Context, c Call) {
	if len(s.exporters) > 0 {
		s.record(c)
		s.trace(c.Trace, c.Spans)
	}
	// The file exporters' line, with every header when content is full.
	var line []byte
	if len(s.files) > 0 {
		line = s.line(c, true)
		for _, f := range s.files {
			if !f.queue.add(line) {
				f.batches.dropped(c.Ledger)
			}
		}
	}
	switch {
	case s.console == nil:
	case s.console.out.JSON:
		// The record every exporter gets, never content: full. It is the
		// file exporters' line when that has no more.
		if line == nil || c.Content != nil {
			line = s.line(c, false)
		}
		s.console.queue.add(terminalSafe(line))
	default:
		if lines := s.console.lines(c); lines != nil {
			s.console.queue.add(lines)
		}
	}
}

// line is the call as one line of JSON.
func (s *Exporters) line(c Call, withContent bool) []byte {
	buf := s.lines.Get().(*bytes.Buffer)
	buf.Reset()
	appendLine(buf, s.resource, c, withContent)
	line := slices.Clone(buf.Bytes())
	s.lines.Put(buf)
	return line
}

// event is the record's event name.
func (c Call) event() string { return cmp.Or(c.Event, CallEvent) }

// LedgerRecord is the call's record as its hash covers it: every field the
// exporters send, as they send it. c.Attrs must not hold the hash yet.
//
// The hash and the two writers, appendRecord and appendLine, read the same
// Call, and the same helpers for what they work out: event, recordFlags
// and the level in c.Severity.
func (s *Exporters) LedgerRecord(c Call) ledger.Record {
	severity := c.Severity
	return ledger.Record{
		Attrs: c.Attrs, At: c.At, Observed: c.Observed,
		TraceID: c.Trace.TraceID().String(), SpanID: c.Trace.SpanID().String(), Flags: recordFlags(c.Trace),
		SeverityNumber: int(severity), SeverityText: severity.String(), Event: c.event(), Body: c.Body,
		Resource: s.resource,
	}
}

// recordFlags are a record's trace flags, as OTLP sends them: none for a
// record with no trace.
func recordFlags(sc trace.SpanContext) uint32 {
	if !sc.IsValid() {
		return 0
	}
	return uint32(sc.TraceFlags())
}

// record queues one log record for every OTLP exporter. It never
// blocks: a full queue refuses the new record, and the ledger notes it.
// The record is written once, for every exporter.
func (s *Exporters) record(c Call) {
	scratch := s.scratch.Get().(*[]byte)
	b := appendRecord((*scratch)[:0], c.event(), c.At, c.Observed, c.Severity, c.Trace, c.Body, c.Attrs)
	written := slices.Clone(b)
	*scratch = b
	s.scratch.Put(scratch)
	for _, d := range s.exporters {
		if !d.queue.add(written) {
			d.batches.dropped(c.Ledger)
		}
	}
}

// Level is a record's level, from its attributes: INFO unless something
// went wrong: WARN when the call or the response
// broke the spec, the vendor refused the call (a 4xx) or a probe found a
// problem, ERROR when the
// vendor failed (a 5xx) or the call failed in the proxy.
func Level(attrs []attribute.KeyValue) log.Severity {
	severity := log.SeverityInfo
	for _, kv := range attrs {
		switch kv.Key {
		case semconv.ErrorTypeKey:
			return log.SeverityError
		case semconv.HTTPResponseStatusCodeKey:
			switch status := kv.Value.AsInt64(); {
			case status >= 500:
				return log.SeverityError
			case status >= 400:
				severity = log.SeverityWarn
			}
		case "hotline.schema.request.errors", "hotline.schema.response.errors":
			severity = log.SeverityWarn
		case "hotline.probe.verdict":
			// A probe that found a problem is a warning; one that found
			// the vendor reachable again is not (Peter, 2026-11-22).
			if kv.Value.AsString() != "reachable_now" {
				severity = log.SeverityWarn
			}
		}
	}
	return severity
}

// Shutdown sends what is still queued, within the time ctx allows.
func (s *Exporters) Shutdown(ctx context.Context) error {
	var errs []error
	// Batch records still being signed are waited for, within the time
	// allowed, then the batch records go after the records they cover.
	signed := make(chan struct{})
	go func() {
		s.signing.Wait()
		close(signed)
	}()
	select {
	case <-signed:
	case <-ctx.Done():
	}
	for _, b := range s.batches {
		errs = append(errs, b.shutdown(ctx))
	}
	for _, d := range s.exporters {
		errs = append(errs, d.queue.Shutdown(ctx), d.spans.Shutdown(ctx))
		d.transport.close()
		if d.lock != nil {
			errs = append(errs, d.lock.Close())
		}
	}
	for _, f := range s.local() {
		errs = append(errs, f.queue.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
