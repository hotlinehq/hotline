package proxy

import (
	"bytes"
	"time"
)

// streamItem is one stream item as it was read: written as JSON for its
// check, its size as sent, and when its first and last bytes passed.
type streamItem struct {
	// json is the stream item as JSON: a Server-Sent Event's fields as an
	// object, or a JSON line or record as sent. Nil when the stream item
	// was over keptBody, so it was not kept.
	json       []byte
	size       int64
	start, end time.Time
}

// itemReader cuts a stream into its stream items as the bytes pass, and
// hands each on as it ends.
type itemReader interface {
	// pass reads bytes that passed at a time.
	pass(p []byte, at time.Time)
	// end says the stream ended whole: a stream item it left open is
	// handed on, or dropped, as its format says.
	end()
}

// newItemReader is the reader of a streamed media type, which hands each
// stream item to delivered.
func newItemReader(mediaType string, delivered func(streamItem)) itemReader {
	switch mediaType {
	case "text/event-stream":
		return &sseReader{delivered: delivered}
	case "application/jsonl", "application/x-ndjson":
		return &lineReader{held: heldItem{delivered: delivered}}
	}
	// application/json-seq and every +json-seq type: the spec loads an
	// itemSchema for no other.
	return &seqReader{held: heldItem{delivered: delivered}}
}

// heldItem is the stream item being read: at most keptBody of it, and its
// size whatever it is.
type heldItem struct {
	delivered func(streamItem)
	open      bool
	// start and last are when its first and last bytes passed.
	start, last time.Time
	size        int64
	tooBig      bool
	text        []byte
}

// add takes one byte of the stream item; partOfJSON says the byte is part
// of its JSON, not a separator.
func (h *heldItem) add(b byte, at time.Time, partOfJSON bool) {
	if !h.open {
		h.open, h.start = true, at
	}
	h.last = at
	h.size++
	if h.size > keptBody {
		h.tooBig, h.text = true, nil
	}
	if partOfJSON && !h.tooBig {
		h.text = append(h.text, b)
	}
}

// deliver hands the stream item on, unless it holds nothing but white
// space, which is no stream item; then it starts the next.
func (h *heldItem) deliver() {
	trimmed := bytes.TrimSpace(h.text)
	if h.open && (h.tooBig || len(trimmed) > 0) {
		e := streamItem{size: h.size, start: h.start, end: h.last}
		if !h.tooBig {
			e.json = append([]byte(nil), trimmed...)
		}
		h.delivered(e)
	}
	h.open, h.size, h.tooBig, h.text = false, 0, false, h.text[:0]
}

// lineReader reads JSON Lines (application/jsonl, application/x-ndjson):
// one stream item per line, ended by a line feed, with a carriage return
// before it allowed. An empty line is no stream item, and the last line
// needs no line end.
type lineReader struct{ held heldItem }

func (r *lineReader) pass(p []byte, at time.Time) {
	for _, b := range p {
		r.held.add(b, at, b != '\n')
		if b == '\n' {
			r.held.deliver()
		}
	}
}

func (r *lineReader) end() { r.held.deliver() }

// seqReader reads JSON sequences (RFC 7464): one stream item per record,
// each starting with the record separator, byte 0x1E, and ending with a
// line feed. A record may span lines; an empty one is no stream item, and
// the last one needs no line feed. Bytes before the first separator are
// read as a record too, so a stream that is no sequence fails its check.
type seqReader struct{ held heldItem }

// recordSeparator starts each record of a JSON sequence.
const recordSeparator = 0x1e

func (r *seqReader) pass(p []byte, at time.Time) {
	for _, b := range p {
		if b == recordSeparator {
			r.held.deliver()
		}
		r.held.add(b, at, b != recordSeparator)
	}
}

func (r *seqReader) end() { r.held.deliver() }
