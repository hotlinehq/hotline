// Package ledger makes the hashes and signed batches that let anyone check
// Hotline's records without a secret (docs/proxy-ledger.md). The proxy uses
// it to make them and hotline-proxy verify to check them, so both build the
// same bytes the same way.
package ledger

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// ErrNotAHash is a text that is not a hash as the ledger writes it.
var ErrNotAHash = errors.New("ledger: not a sha256 hash")

// hashPrefix starts every hash the ledger writes, so a reader knows how it
// was made.
const hashPrefix = "sha256:"

// The formats, each the first piece of what it covers, so bytes of one kind
// can never pass for another.
const (
	recordFormat  = "hotline.record.v1"
	traceFormat   = "hotline.trace.v1"
	bodyFormat    = "hotline.body.v1"
	contentFormat = "hotline.content.v1"
	// BatchFormat is the format of a batch record.
	BatchFormat = "hotline.batch.v1"
)

// writer feeds bytes into a SHA-256, through a small buffer, so a hash makes
// no text of its own. Each piece is its length as 8 bytes, then its bytes;
// each entry is its name as a piece, then its value. Nothing is quoted or
// escaped: the lengths say where each piece ends.
type writer struct {
	h   hash.Hash
	buf [512]byte
	n   int
	// sorted and spans are room to sort attributes and spans in, kept
	// with the writer so a reused writer sorts without making a new list,
	// and the caller's own list is never changed.
	sorted []attribute.KeyValue
	spans  []Span
}

func newWriter() *writer { return &writer{h: sha256.New()} }

// Hasher makes the ledger's hashes. It keeps the writers it is done with
// and uses them again, so a busy proxy does not make a new writer and a new
// SHA-256 for every hash. The zero value is ready to use, and it is safe
// for many goroutines at once.
type Hasher struct{ writers sync.Pool }

// writer is a writer with nothing written yet: a kept one, or a new one.
func (h *Hasher) writer() *writer {
	if w, ok := h.writers.Get().(*writer); ok {
		return w
	}
	return newWriter()
}

// sum is the SHA-256 of everything w wrote. w is kept for the next hash,
// so the caller must not use it again.
func (h *Hasher) sum(w *writer) [sha256.Size]byte {
	out := w.sum()
	w.h.Reset()
	clear(w.sorted)
	w.sorted = w.sorted[:0]
	clear(w.spans)
	w.spans = w.spans[:0]
	h.writers.Put(w)
	return out
}

func (w *writer) write(p []byte) {
	for len(p) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		c := copy(w.buf[w.n:], p)
		w.n += c
		p = p[c:]
	}
}

func (w *writer) writeString(p string) {
	for len(p) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		c := copy(w.buf[w.n:], p)
		w.n += c
		p = p[c:]
	}
}

func (w *writer) flush() {
	w.h.Write(w.buf[:w.n])
	w.n = 0
}

func (w *writer) byte(b byte) { w.write([]byte{b}) }

func (w *writer) uint(u uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], u)
	w.write(b[:])
}

func (w *writer) piece(p string) {
	w.uint(uint64(len(p)))
	w.writeString(p)
}

// sum is the SHA-256 of everything written.
func (w *writer) sum() [sha256.Size]byte {
	w.flush()
	var out [sha256.Size]byte
	w.h.Sum(out[:0])
	return out
}

// entry is an attribute: its name, then its value.
func (w *writer) entry(kv attribute.KeyValue) {
	w.piece(string(kv.Key))
	w.value(kv.Value)
}

// value is a type letter, then: text as a piece; a whole or decimal number
// as 8 bytes; true or false as one byte; a list or map as its count in 8
// bytes, then each item, a map's in name order. A list of texts and a list
// of any values are written the same way, as JSON cannot tell them apart.
func (w *writer) value(v attribute.Value) {
	switch v.Type() {
	case attribute.STRING:
		w.byte('s')
		w.piece(v.AsString())
	case attribute.INT64:
		w.byte('i')
		w.uint(uint64(v.AsInt64())) //nolint:gosec // the bits as they are
	case attribute.FLOAT64:
		w.byte('f')
		w.uint(math.Float64bits(v.AsFloat64()))
	case attribute.BOOL:
		w.byte('b')
		if v.AsBool() {
			w.byte(1)
		} else {
			w.byte(0)
		}
	case attribute.STRINGSLICE:
		items := v.AsStringSlice()
		w.list(len(items))
		for _, item := range items {
			w.value(attribute.StringValue(item))
		}
	case attribute.INT64SLICE:
		items := v.AsInt64Slice()
		w.list(len(items))
		for _, item := range items {
			w.value(attribute.Int64Value(item))
		}
	case attribute.FLOAT64SLICE:
		items := v.AsFloat64Slice()
		w.list(len(items))
		for _, item := range items {
			w.value(attribute.Float64Value(item))
		}
	case attribute.BOOLSLICE:
		items := v.AsBoolSlice()
		w.list(len(items))
		for _, item := range items {
			w.value(attribute.BoolValue(item))
		}
	case attribute.SLICE:
		items := v.AsSlice()
		w.list(len(items))
		for _, item := range items {
			w.value(item)
		}
	case attribute.MAP:
		entries := slices.SortedFunc(slices.Values(v.AsMap()), byKey)
		w.byte('m')
		w.uint(uint64(len(entries)))
		for _, kv := range entries {
			w.entry(kv)
		}
	default:
		// Nothing else is ever hashed; a mark keeps it from passing for
		// something else.
		w.byte('?')
	}
}

// spanID is an entry of a span id as text, as entry would write it, the
// hex made in place: empty for no span.
func (w *writer) spanID(name string, id SpanID) {
	w.piece(name)
	w.byte('s')
	if id == (SpanID{}) {
		w.piece("")
		return
	}
	var text [2 * len(SpanID{})]byte
	hex.Encode(text[:], id[:])
	w.uint(uint64(len(text)))
	w.write(text[:])
}

func (w *writer) list(n int) {
	w.byte('l')
	w.uint(uint64(n)) //nolint:gosec // a count is never negative
}

// entries writes attributes sorted by name. Attributes already in order
// are written as they are; others are sorted in the writer's own room, so
// the caller's list is never changed.
func (w *writer) entries(attrs []attribute.KeyValue) {
	if !slices.IsSortedFunc(attrs, byKey) {
		w.sorted = append(w.sorted[:0], attrs...)
		slices.SortFunc(w.sorted, byKey)
		attrs = w.sorted
	}
	for _, kv := range attrs {
		w.entry(kv)
	}
}

func byKey(a, b attribute.KeyValue) int { return strings.Compare(string(a.Key), string(b.Key)) }

// Text writes a hash as the ledger does: sha256: and its hex.
func Text(sum [sha256.Size]byte) string { return hashPrefix + hex.EncodeToString(sum[:]) }

// Parse reads a hash written by Text.
func Parse(text string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	hexText, found := strings.CutPrefix(text, hashPrefix)
	if !found || len(hexText) != 2*sha256.Size {
		return out, ErrNotAHash
	}
	if _, err := hex.Decode(out[:], []byte(hexText)); err != nil {
		return out, ErrNotAHash
	}
	return out, nil
}

// Record is a record as its hash covers it: its attributes, without the
// hash itself, and its time, trace id and span id. Times are to the
// microsecond, as the file exporter writes them.
type Record struct {
	Attrs   []attribute.KeyValue
	At      time.Time
	TraceID string
	SpanID  string
}

// RecordHash is the hash of a record: the word hotline.record.v1, every
// attribute sorted by name, then the time in microseconds since 1970, the
// trace id and the span id as hex. It is the one-off form, for verify; the
// proxy reuses writers through a Hasher.
func RecordHash(r Record) [sha256.Size]byte { return (&Hasher{}).Record(r) }

// Record is RecordHash, with a reused writer.
func (h *Hasher) Record(r Record) [sha256.Size]byte {
	w := h.writer()
	w.piece(recordFormat)
	w.entries(r.Attrs)
	w.entry(attribute.Int64("log.record.timestamp", r.At.UnixMicro()))
	w.entry(attribute.String("log.record.trace_id", r.TraceID))
	w.entry(attribute.String("log.record.span_id", r.SpanID))
	return h.sum(w)
}

// RecordCovers says whether a record's hash covers the attribute named name
// in the record's file line: every attribute but the hash itself, the
// trace's spans (hotline.trace.spans, which have their own hash), and what
// content: full adds under hotline.content.*, which hotline.content.hash
// covers. The file exporter checks in its tests that it adds nothing else.
func RecordCovers(name string) bool {
	switch {
	case name == "hotline.record.hash", name == "hotline.trace.spans":
		return false
	case strings.HasPrefix(name, "hotline.content."):
		return name == "hotline.content.hash"
	}
	return true
}

// ErrNotASpanID is a text that is not a span id as the proxy writes it.
var ErrNotASpanID = errors.New("ledger: not a span id")

// SpanID is a span's id as its 8 bytes. The zero id is no span: the parent
// of a trace's first span. It is hashed as the proxy writes it, 16 small hex
// letters, or empty for no span, made in the writer without a text of its
// own.
type SpanID [8]byte

// ParseSpanID reads a span id as the proxy writes it, empty for no span.
// Any other text, capital hex letters or zeros too, is refused: the proxy
// never writes it, so a file holding it was changed.
func ParseSpanID(text string) (SpanID, error) {
	var id SpanID
	if text == "" {
		return id, nil
	}
	if len(text) != 2*len(id) || strings.ToLower(text) != text {
		return id, ErrNotASpanID
	}
	if _, err := hex.Decode(id[:], []byte(text)); err != nil || id == (SpanID{}) {
		return SpanID{}, ErrNotASpanID
	}
	return id, nil
}

// Span is one step of a call's trace, as its hash covers it.
type Span struct {
	ID, Parent SpanID
	Name, Kind string
	Start, End time.Time
	Status     string
	Attrs      []attribute.KeyValue
}

// TraceHash is the hash of a call's trace: the word hotline.trace.v1, the
// trace id, then each span in start order, a tie going by span id: its span
// id, parent span id (empty for none), name, kind, start and end in
// microseconds, status and number of attributes, then its attributes sorted
// by name. It is the one-off form, for verify; the proxy reuses writers
// through a Hasher.
func TraceHash(traceID string, spans []Span) [sha256.Size]byte {
	return (&Hasher{}).Trace(traceID, spans)
}

// Trace is TraceHash, with a reused writer.
func (h *Hasher) Trace(traceID string, spans []Span) [sha256.Size]byte {
	w := h.writer()
	w.spans = append(w.spans[:0], spans...)
	slices.SortFunc(w.spans, func(a, b Span) int {
		if c := cmp.Compare(a.Start.UnixMicro(), b.Start.UnixMicro()); c != 0 {
			return c
		}
		// Hex keeps the order of the bytes, so this is the order of the
		// ids as text.
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	w.piece(traceFormat)
	w.entry(attribute.String("trace.trace_id", traceID))
	for _, s := range w.spans {
		w.spanID("span.span_id", s.ID)
		w.spanID("span.parent_span_id", s.Parent)
		w.entry(attribute.String("span.name", s.Name))
		w.entry(attribute.String("span.kind", s.Kind))
		w.entry(attribute.Int64("span.start_time", s.Start.UnixMicro()))
		w.entry(attribute.Int64("span.end_time", s.End.UnixMicro()))
		w.entry(attribute.String("span.status", s.Status))
		w.entry(attribute.Int("span.attribute_count", len(s.Attrs)))
		w.entries(s.Attrs)
	}
	return h.sum(w)
}

// ContentHash is the hash of what content: full adds to a record, the
// bodies apart, as they have their own hashes: the word hotline.content.v1,
// then every attribute sorted by name. It is the one-off form, for verify;
// the proxy reuses writers through a Hasher.
func ContentHash(attrs []attribute.KeyValue) [sha256.Size]byte {
	return (&Hasher{}).Content(attrs)
}

// Content is ContentHash, with a reused writer.
func (h *Hasher) Content(attrs []attribute.KeyValue) [sha256.Size]byte {
	w := h.writer()
	w.piece(contentFormat)
	w.entries(attrs)
	return h.sum(w)
}

// BodyHash hashes one body as it passes. Write the body's bytes to it, then
// call Sum.
type BodyHash struct {
	h *Hasher
	w *writer
	// text is the hash, once Sum has worked it out and let the writer go.
	text string
}

// NewBodyHash starts a body's hash: the word hotline.body.v1, the side
// (request or response), the trace id's 16 bytes and the span id's 8 bytes.
// The ids are a public salt: they keep two calls with the same body from
// having the same hash. It is the one-off form, for verify; the proxy
// reuses writers through a Hasher.
func NewBodyHash(side string, traceID [16]byte, spanID [8]byte) *BodyHash {
	return (&Hasher{}).Body(side, traceID, spanID)
}

// Body is NewBodyHash, with a reused writer: Sum hands it back.
func (h *Hasher) Body(side string, traceID [16]byte, spanID [8]byte) *BodyHash {
	w := h.writer()
	w.piece(bodyFormat)
	w.piece(side)
	w.write(traceID[:])
	w.write(spanID[:])
	return &BodyHash{h: h, w: w}
}

// Write adds body bytes. It never fails. Bytes written after Sum are not
// in the hash: the proxy can still be reading a request body when the
// call's record takes its hash.
func (b *BodyHash) Write(p []byte) (int, error) {
	if b.w != nil {
		b.w.write(p)
	}
	return len(p), nil
}

// Sum is the body's hash as text, and hands the writer back to its Hasher.
// Asking again gives the same text.
func (b *BodyHash) Sum() string {
	if b.w != nil {
		b.text = Text(b.h.sum(b.w))
		b.w = nil
	}
	return b.text
}
