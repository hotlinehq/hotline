package proxy

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"
)

// Server-Sent Events, read as they pass, by the rules of the HTML standard
// (https://html.spec.whatwg.org/multipage/server-sent-events.html, "Parsing
// an event stream"), as OpenAPI 3.2 asks: lines end with CR LF, LF or CR; a
// line starting with a colon is a comment; a blank line ends an event; the
// data lines of an event are joined with a line break; an event with no
// data is never delivered; a field the standard doesn't know is ignored.

// sseEvent is one event that was delivered: its fields as OpenAPI 3.2
// reads them, written as a JSON object for the check, its size as sent,
// and when its first and last bytes passed.
type sseEvent struct {
	// item is the event as JSON; nil when the event was over keptBody, so
	// it was not kept.
	item       []byte
	size       int64
	start, end time.Time
}

// sseReader cuts a stream into its events as the bytes pass. It keeps only
// the event being read, at most keptBody of it, and hands each event on as
// it ends.
type sseReader struct {
	// delivered is told each event as it ends.
	delivered func(sseEvent)

	// started says a byte of the stream passed; the first may be a byte
	// order mark, which is dropped.
	started bool
	bom     int
	// afterCR says the last byte was a CR, so a LF right after it is the
	// same line end.
	afterCR bool
	// line is the line being read, kept while the event fits.
	line []byte
	// lineStart is the start of the line, kept always, to tell a data line
	// from others once the event no longer fits.
	lineStart []byte
	// blank says the line just ended was blank.
	blank bool

	// The event being read.
	open    bool
	start   time.Time
	size    int64
	tooBig  bool
	hasData bool
	data    []byte
	name    *string
	id      *string
	retry   *int64
}

// bom is the UTF-8 byte order mark that may start a stream.
const bom = "\xef\xbb\xbf"

// pass reads bytes that passed at a time.
func (r *sseReader) pass(p []byte, at time.Time) {
	for _, b := range p {
		if r.skipMark(b) {
			continue
		}
		if b == '\n' && r.afterCR && !r.open {
			// The LF of the CR LF that ended the last event.
			r.afterCR = false
			continue
		}
		if !r.open {
			r.open, r.start = true, at
		}
		r.size++
		if r.size > keptBody && !r.tooBig {
			// What is kept is let go: the event is only counted from here.
			r.tooBig, r.line, r.data = true, nil, nil
		}
		switch {
		case b == '\n' && r.afterCR:
			// The LF of a CR LF: the line already ended at the CR.
			r.afterCR = false
		case b == '\n' || b == '\r':
			r.afterCR = b == '\r'
			r.endLine()
			r.ending(at)
		default:
			r.afterCR = false
			if !r.tooBig {
				r.line = append(r.line, b)
			}
			if len(r.lineStart) < len("retry:") {
				r.lineStart = append(r.lineStart, b)
			}
		}
	}
}

// skipMark drops a byte order mark at the very start of the stream, and
// says whether b was part of it.
func (r *sseReader) skipMark(b byte) bool {
	if r.started {
		return false
	}
	if b == bom[r.bom] {
		r.bom++
		if r.bom == len(bom) {
			r.started = true
		}
		return true
	}
	// Not a mark after all: the bytes taken for one are the stream's own.
	r.started = true
	taken := []byte(bom[:r.bom])
	r.bom = 0
	for _, t := range taken {
		r.line = append(r.line, t)
		r.lineStart = append(r.lineStart, t)
		r.size++
	}
	return false
}

// ending ends the event, after a line end, when that line was blank.
func (r *sseReader) ending(at time.Time) {
	if !r.blank {
		return
	}
	r.blank = false
	if r.hasData {
		r.deliver(at)
	}
	r.reset()
}

// endLine reads the field of the line just ended.
func (r *sseReader) endLine() {
	line, start := r.line, r.lineStart
	r.line, r.lineStart = r.line[:0], r.lineStart[:0]
	if len(start) == 0 {
		r.blank = true
		return
	}
	if start[0] == ':' {
		return
	}
	if r.tooBig {
		// Only whether the event has data still matters.
		if bytes.HasPrefix(start, []byte("data:")) || string(start) == "data" {
			r.hasData = true
		}
		return
	}
	name, value, found := bytes.Cut(line, []byte(":"))
	if found && len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(name) {
	case "event":
		v := string(value)
		r.name = &v
	case "data":
		r.data = append(append(r.data, value...), '\n')
		r.hasData = true
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			v := string(value)
			r.id = &v
		}
	case "retry":
		if n, err := strconv.ParseInt(string(value), 10, 64); err == nil && n >= 0 && digits(value) {
			r.retry = &n
		}
	}
}

// digits says whether every byte is an ASCII digit, as retry must be.
func digits(value []byte) bool {
	for _, b := range value {
		if b < '0' || b > '9' {
			return false
		}
	}
	return len(value) > 0
}

// deliver hands the event on, written as the object OpenAPI 3.2 checks.
func (r *sseReader) deliver(at time.Time) {
	e := sseEvent{size: r.size, start: r.start, end: at}
	if !r.tooBig {
		e.item, _ = json.Marshal(struct { //nolint:errchkjson // text, text pointers and a number always encode
			Data  string  `json:"data"`
			Event *string `json:"event,omitempty"`
			ID    *string `json:"id,omitempty"`
			Retry *int64  `json:"retry,omitempty"`
		}{Data: string(bytes.TrimSuffix(r.data, []byte("\n"))), Event: r.name, ID: r.id, Retry: r.retry})
	}
	r.delivered(e)
}

// reset starts the next event.
func (r *sseReader) reset() {
	r.open, r.size, r.tooBig, r.hasData = false, 0, false, false
	r.data, r.name, r.id, r.retry = r.data[:0], nil, nil, nil
}
