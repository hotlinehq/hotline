package proxy

import (
	"bytes"
	"mime"
	"strconv"
	"strings"
)

// hideFileNames is the copy content: full keeps of a multipart/form-data
// request body, sent as contentType: the bytes as they passed, but with
// each part's file name REDACTED, unless shown says the spec shows that
// part's file name (Peter, 2026-11-26, Q1 and Q2). Where the parts can no
// longer be read, because the body broke or was cut at maxBodySize, the
// copy stops before the part that can't be read, so no file name the
// proxy could not find is kept (Q3). It reads each part's headers as
// partReader does, with readPartHeaders. redacted says a file name was
// hidden, and cut that the copy stops before the kept bytes end.
func hideFileNames(kept []byte, contentType string, shown func(partName string) bool) (hidden []byte, redacted, cut bool) {
	_, params, _ := mime.ParseMediaType(contentType)
	boundary := params["boundary"]
	if boundary == "" || len(boundary) > maxBoundary {
		return nil, false, len(kept) > 0
	}
	f := formCopy{kept: kept, delimiter: []byte("\r\n--" + boundary), shown: shown}
	end := f.read()
	if len(f.edits) == 0 && end == len(kept) {
		return kept, false, false
	}
	return f.write(end), len(f.edits) > 0, end < len(kept)
}

// formCopy finds, in the kept bytes of a multipart body, the file names to
// hide.
type formCopy struct {
	kept      []byte
	delimiter []byte
	shown     func(partName string) bool
	// edits are the pieces of kept to replace, in order.
	edits []edit
}

// edit replaces kept[from:to] with with.
type edit struct {
	from, to int
	with     string
}

// read finds the file names to hide, and returns where the copy ends: the
// end of kept, or the start of the delimiter before the first part that
// can't be read.
func (f *formCopy) read() int {
	// The first delimiter may start the body with no line break before
	// it, as partReader reads it.
	at := 0
	next := len(f.delimiter) - 2
	if !bytes.HasPrefix(f.kept, f.delimiter[2:]) {
		at = bytes.Index(f.kept, f.delimiter)
		if at < 0 {
			return 0
		}
		next = at + len(f.delimiter)
	}
	for parts := 0; ; parts++ {
		rest := f.kept[next:]
		if bytes.HasPrefix(rest, []byte("--")) {
			// The last part ended: what follows is the epilogue, kept.
			return len(f.kept)
		}
		// partReader holds a delimiter's line, its line break included,
		// to at most maxDelimiterLine bytes.
		lineEnd := bytes.Index(rest[:min(len(rest), maxDelimiterLine)], []byte("\r\n"))
		if lineEnd < 0 || len(bytes.Trim(rest[:lineEnd], " \t")) > 0 || parts == maxParts {
			return at
		}
		// The headers start at the line break ending the delimiter's
		// line, so a part with no headers is found the same way.
		headers := next + lineEnd
		headersEnd := bytes.Index(f.kept[headers:min(len(f.kept), headers+maxPartHeaders)], []byte("\r\n\r\n"))
		if headersEnd < 0 || !f.hide(headers+2, headers+headersEnd+2) {
			return at
		}
		// The line break of the empty line after the headers may start
		// the next delimiter, as in partReader.
		content := headers + headersEnd + 2
		found := bytes.Index(f.kept[content:], f.delimiter)
		if found < 0 {
			// The body ends in this part's content: the file's bytes,
			// which the copy keeps.
			return len(f.kept)
		}
		at = content + found
		next = at + len(f.delimiter)
	}
}

// hide adds the edits that hide the file name of the part whose headers
// are kept[from:to], each line with its line break. It says false when the
// headers can't be read as partReader reads them, or the file name can't
// be hidden for sure.
func (f *formCopy) hide(from, to int) bool {
	block := f.kept[from:to]
	if bytes.Count(block, []byte("\n")) != bytes.Count(block, []byte("\r\n")) {
		// A line ended by a line feed alone: Go's readers end the
		// headers there, partReader does not, so where the part starts
		// is unsure.
		return false
	}
	_, params, ok := readPartHeaders(block)
	if !ok || f.shown(params["name"]) {
		return ok
	}
	var edits []edit
	folded := false
	for start := 0; start < len(block); {
		end := start + bytes.Index(block[start:], []byte("\r\n"))
		line := block[start:end]
		// A header folded onto a second line is rare, and too unsure to
		// hide in place.
		folded = folded || line[0] == ' ' || line[0] == '\t'
		header, _, _ := strings.Cut(string(line), ":")
		if strings.EqualFold(strings.TrimSpace(header), "Content-Disposition") {
			edits = append(edits, fileNameEdits(line, from+start)...)
		}
		start = end + 2
	}
	// The hidden headers are read again, so a file name the edits missed
	// stops the copy instead of being kept.
	var after []byte
	last := from
	for _, e := range edits {
		after = append(append(after, f.kept[last:e.from]...), e.with...)
		last = e.to
	}
	after = append(after, f.kept[last:to]...)
	_, params, ok = readPartHeaders(after)
	sure := ok && !folded && (params["filename"] == "" || params["filename"] == redactedName)
	if sure {
		f.edits = append(f.edits, edits...)
	}
	return sure
}

// redactedName is what the copy says in place of a file name.
const redactedName = "REDACTED"

// fileNameEdits are the edits that hide every file name parameter of one
// Content-Disposition line, which starts at offset in the kept bytes:
// filename, filename* (RFC 6266) and its pieces filename*0, filename*1 and
// so on (RFC 2231). The first piece reads REDACTED and the others go.
func fileNameEdits(line []byte, offset int) []edit {
	var edits []edit
	for i := bytes.IndexByte(line, ';'); i >= 0 && i < len(line); {
		paramStart := i
		i++
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		nameStart := i
		for i < len(line) && line[i] != '=' && line[i] != ';' {
			i++
		}
		if i == len(line) || line[i] == ';' {
			continue
		}
		attribute := strings.ToLower(strings.TrimSpace(string(line[nameStart:i])))
		i++
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		valueStart := i
		i = valueEnd(line, i)
		with, ok := hiddenValue(attribute)
		switch {
		case !ok:
		case with == "":
			edits = append(edits, edit{offset + paramStart, offset + i, ""})
		default:
			edits = append(edits, edit{offset + valueStart, offset + i, with})
		}
		for i < len(line) && line[i] != ';' {
			i++
		}
	}
	return edits
}

// valueEnd is where a parameter's value that starts at i ends: after its
// closing quote, or at the next semicolon.
func valueEnd(line []byte, i int) int {
	if i < len(line) && line[i] == '"' {
		for i++; i < len(line); i++ {
			switch line[i] {
			case '\\':
				i++
			case '"':
				return i + 1
			}
		}
		return len(line)
	}
	for i < len(line) && line[i] != ';' && line[i] != ' ' && line[i] != '\t' {
		i++
	}
	return i
}

// hiddenValue is the value that hides a file name parameter named
// attribute, or "" when the parameter goes; ok is false for any other
// parameter.
func hiddenValue(attribute string) (with string, ok bool) {
	switch {
	case attribute == "filename", attribute == "filename*0":
		return `"` + redactedName + `"`, true
	case attribute == "filename*", attribute == "filename*0*":
		return "UTF-8''" + redactedName, true
	case strings.HasPrefix(attribute, "filename*"):
		piece := strings.TrimSuffix(strings.TrimPrefix(attribute, "filename*"), "*")
		if _, err := strconv.Atoi(piece); err == nil {
			return "", true
		}
	}
	return "", false
}

// write is the copy: the kept bytes up to end, with the edits made.
func (f *formCopy) write(end int) []byte {
	hidden := make([]byte, 0, end)
	last := 0
	// Every edit is in a part the copy keeps, before end.
	for _, e := range f.edits {
		hidden = append(append(hidden, f.kept[last:e.from]...), e.with...)
		last = e.to
	}
	return append(hidden, f.kept[last:end]...)
}
