package proxy

import (
	"bufio"
	"bytes"
	"mime"
	"net/textproto"

	"hotline-proxy/internal/spec"
)

// partReader reads a multipart/form-data request body part by part as it
// passes, so the check sees every text part of an upload of any size while
// the proxy never holds a file (Peter, 2026-11-24, M4, Q1 to Q3). It keeps
// the text parts' content, at most limit bytes of it in all, and only
// counts the bytes of the parts the spec calls files. The text parts' bytes
// all go in one buffer borrowed from the pool, and each part's Value is a
// piece of it, given once the parts are handed (Peter, 2026-11-24). Only
// the tap uses it, under the tap's lock.
type partReader struct {
	// delimiter starts every part and ends the last: a line break, two
	// dashes and the boundary (RFC 2046).
	delimiter []byte
	isFile    func(name string) bool
	limit     int
	state     partState
	// matched is how many bytes of a delimiter the bytes so far end with.
	matched int
	// assumedLineBreak says the first two matched bytes, the line break
	// that starts a delimiter, were not read but assumed: see
	// expectDelimiterNext.
	assumedLineBreak bool
	// head is the line after a delimiter, or a part's headers, read so far.
	head []byte
	// textKeeper keeps the bytes of every text part, one after the other.
	textKeeper bodyKeeper
	// text are the text parts, files the file parts, each in the order they
	// came. current is the part being read, in one or the other. A text
	// part's Value is set only when the parts are handed, as the buffer may
	// move to a larger one until then.
	text []spec.FormPart
	// textStarts says where each text part's bytes start in textKeeper.
	textStarts []int
	files      []spec.FormPart
	current    *spec.FormPart
	// inFile says current is a file part, whose bytes are only counted.
	inFile bool
	// seen says a byte came, so an empty body is handed as no body.
	seen bool
	// handed is set once the parts have gone to the check; from then on
	// they never change.
	handed bool
}

type partState int

const (
	beforeFirstPart partState = iota
	afterDelimiter
	inHeaders
	inContent
	afterLastPart
	broken
)

// The limits on what a part may hold before the body is called broken,
// so a body made to fill the memory can't.
const (
	maxPartHeaders = 16 << 10
	// maxDelimiterLine is how many spaces and tabs a delimiter's line may
	// hold before its line break.
	maxDelimiterLine = 256
	maxParts         = 1000
	// maxBoundary is RFC 2046's limit.
	maxBoundary = 70
)

// newPartReader reads a body sent with the multipart contentType, keeping
// its text parts in a buffer borrowed from pool. A body whose boundary is
// missing or too long is broken from its first byte.
func newPartReader(contentType string, isFile func(name string) bool, limit int, pool *bodyPool) *partReader {
	r := &partReader{isFile: isFile, limit: limit, textKeeper: bodyKeeper{limit: limit, pool: pool, expect: -1}}
	_, params, _ := mime.ParseMediaType(contentType)
	boundary := params["boundary"]
	if boundary == "" || len(boundary) > maxBoundary {
		r.state = broken
		return r
	}
	r.delimiter = []byte("\r\n--" + boundary)
	r.expectDelimiterNext()
	return r
}

func (r *partReader) add(p []byte) {
	r.seen = r.seen || len(p) > 0
	for len(p) > 0 && !r.handed {
		switch r.state {
		case beforeFirstPart, inContent:
			p = r.scan(p)
		case afterDelimiter:
			p = r.delimiterLine(p)
		case inHeaders:
			p = r.headers(p)
		default:
			// After the last part, or broken: the rest is not read.
			return
		}
	}
}

// scan passes content on to the current part until a delimiter, and
// returns what comes after it. matched carries a delimiter's start from one
// read to the next. A boundary never holds a carriage return (RFC 2046),
// so a delimiter can start again only at the byte that broke a match.
func (r *partReader) scan(p []byte) []byte {
	for i := 0; i < len(p); {
		if r.matched == 0 {
			next := bytes.IndexByte(p[i:], '\r')
			if next < 0 {
				r.content(p[i:])
				return nil
			}
			r.content(p[i : i+next])
			i += next
		}
		n := min(len(p)-i, len(r.delimiter)-r.matched)
		switch {
		case bytes.Equal(p[i:i+n], r.delimiter[r.matched:r.matched+n]):
			r.matched += n
			i += n
			if r.matched == len(r.delimiter) {
				r.matched, r.assumedLineBreak = 0, false
				r.state = afterDelimiter
				return p[i:]
			}
		case r.matched > 0:
			// What matched so far was content after all, apart from an
			// assumed line break; the byte that broke the match is looked
			// at again.
			from := 0
			if r.assumedLineBreak {
				from = 2
			}
			r.content(r.delimiter[from:r.matched])
			r.matched, r.assumedLineBreak = 0, false
		default:
			r.content(p[i : i+1])
			i++
		}
	}
	return nil
}

// content is bytes of the current part; before the first part they are
// the preamble, which is not read.
func (r *partReader) content(b []byte) {
	if r.state != inContent || len(b) == 0 {
		return
	}
	part := r.current
	part.Size += int64(len(b))
	if r.inFile || part.TooLarge {
		return
	}
	keeper := &r.textKeeper
	if len(keeper.kept)+len(b) > r.limit {
		// The text parts together are over the limit: this one is not
		// kept, and the check skips it. Its bytes are the last kept, so
		// they are let go.
		keeper.cut(r.textStarts[len(r.textStarts)-1])
		part.TooLarge = true
		return
	}
	keeper.keep(b)
}

// delimiterLine reads the rest of a delimiter's line, one byte at a time,
// as the line is short: two dashes after the last part, or spaces and a
// line break before a part's headers.
func (r *partReader) delimiterLine(p []byte) []byte {
	c := p[0]
	r.head = append(r.head, c)
	switch {
	case string(r.head) == "--":
		r.state = afterLastPart
		return nil
	case c == '\n' && string(bytes.TrimLeft(r.head, " \t")) == "\r\n":
		// Headers end with an empty line; starting them with a line
		// break finds a part that has none the same way.
		r.head = append(r.head[:0], '\r', '\n')
		r.state = inHeaders
	case c == '\n', len(r.head) > 2 && c != ' ' && c != '\t' && c != '\r', len(r.head) > maxDelimiterLine:
		r.state = broken
		return nil
	}
	return p[1:]
}

// headers reads a part's headers up to the empty line after them, starts
// the part, and returns its first content.
func (r *partReader) headers(p []byte) []byte {
	before := len(r.head)
	r.head = append(r.head, p[:min(len(p), maxPartHeaders-before)]...)
	end := bytes.Index(r.head, []byte("\r\n\r\n"))
	if end < 0 {
		if len(r.head) >= maxPartHeaders {
			r.state = broken
		}
		return nil
	}
	r.start(r.head[2 : end+2])
	r.head = r.head[:0]
	return p[end+4-before:]
}

// start begins a part from its headers.
func (r *partReader) start(block []byte) {
	header, params, ok := readPartHeaders(block)
	if !ok || len(r.text)+len(r.files) == maxParts {
		r.state = broken
		return
	}
	part := spec.FormPart{Name: params["name"], FileName: params["filename"], ContentType: header.Get("Content-Type")}
	list := &r.text
	if r.inFile = r.isFile(part.Name); r.inFile {
		list = &r.files
	} else {
		r.textStarts = append(r.textStarts, len(r.textKeeper.kept))
	}
	*list = append(*list, part)
	r.current = &(*list)[len(*list)-1]
	r.state = inContent
	r.expectDelimiterNext()
}

// readPartHeaders reads a part's headers, block, each line with its line
// break, and the parameters of its Content-Disposition. A part must say it
// is form data and give its name (RFC 7578); ok is false when it does not,
// or the headers can't be read. The check and the copy content: full keeps
// both read the headers here, so they find the same parts.
func readPartHeaders(block []byte) (header textproto.MIMEHeader, params map[string]string, ok bool) {
	header, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(block[:len(block):len(block)], '\r', '\n')))).ReadMIMEHeader()
	disposition, params, dispositionErr := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err != nil || dispositionErr != nil || disposition != "form-data" || params["name"] == "" {
		return nil, nil, false
	}
	return header, params, true
}

// expectDelimiterNext reads on as if the line break that starts a delimiter
// had just come. A delimiter may come at the very start of the body, with no
// line break before it, or right after a part's headers, whose own last line
// break then also starts it. Go's multipart reader reads such a part as
// empty, and so does this one, so the check sees what a vendor written in Go
// sees.
func (r *partReader) expectDelimiterNext() {
	r.matched, r.assumedLineBreak = 2, true
}

// hand gives the parts to the check; they never change after. A body that
// ended before its last part is broken; an empty body is no body.
func (r *partReader) hand() spec.Body {
	r.handed = true
	if !r.seen {
		return spec.Body{}
	}
	kept := r.textKeeper.kept
	for i := range r.text {
		if p := &r.text[i]; !p.TooLarge {
			start := r.textStarts[i]
			p.Value = kept[start : start+int(p.Size) : start+int(p.Size)]
		}
	}
	return spec.Body{Parts: &spec.FormParts{Text: r.text, Files: r.files, Broken: r.state != afterLastPart}}
}

// held is the memory the text parts and the reading take.
func (r *partReader) held() int64 {
	return int64(cap(r.head)) + r.textKeeper.held()
}

// drop gives the text parts' buffer back, and keeps the file parts for the
// record.
func (r *partReader) drop() {
	r.textKeeper.drop()
	r.text, r.textStarts, r.current, r.handed = nil, nil, nil, true
}
