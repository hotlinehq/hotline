package proxy

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// hideFileNames is the copy content: full keeps of a multipart/form-data
// request body, sent as contentType, with each part's file name REDACTED
// unless shown says the spec shows that part's file name (Peter,
// 2026-11-26, Q1 and Q2). Go's own multipart reader reads the parts, as a
// vendor written in Go reads them, and Go's writer writes them again: each
// part's content as sent, its headers sorted, the text before the first
// part and after the last left out (Peter, 2026-11-26, chosen over a
// byte-for-byte copy for its far smaller code). Where a part can't be read,
// because the body broke or was cut at maxBodySize, the copy stops there, so
// no file name the proxy could not find is kept (Q3). redacted says a file
// name was hidden, and cut that the copy stops before the body's end.
func hideFileNames(kept []byte, contentType string, shown func(partName string) bool) (hidden []byte, redacted, cut bool) {
	_, params, _ := mime.ParseMediaType(contentType)
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	if len(kept) == 0 || w.SetBoundary(params["boundary"]) != nil {
		return nil, false, len(kept) > 0
	}
	r := multipart.NewReader(bytes.NewReader(kept), params["boundary"])
	closed := endsWithClosingLine(kept, params["boundary"])
	for {
		// A raw part's content is the bytes as sent, never decoded.
		part, err := r.NextRawPart()
		// Go's reader also says io.EOF when a body ends inside a part's
		// headers, so the body ended only when its last delimiter is the
		// closing one.
		if errors.Is(err, io.EOF) && closed {
			_ = w.Close()
			return out.Bytes(), redacted, false
		}
		if err != nil {
			return out.Bytes(), redacted, true
		}
		header, hid, ok := hiddenHeader(part.Header, shown)
		if !ok {
			return out.Bytes(), redacted, true
		}
		redacted = redacted || hid
		written, _ := w.CreatePart(header)
		if _, err := io.Copy(written, part); err != nil {
			// The body ends in this part's content: the bytes that came
			// are kept.
			return out.Bytes(), redacted, true
		}
	}
}

// redactedName is what the copy says in place of a file name.
const redactedName = "REDACTED"

// hiddenHeader is a part's header with its file name REDACTED, unless
// shown says the spec shows it; hid says it was hidden. Go reads every way
// to write a file name (filename, filename* and its RFC 2231 pieces) into
// one filename, which is written again as one. ok is false when the part has
// no Content-Disposition it can read, so its file name can't be found, as
// RFC 7578 asks every part to have one.
func hiddenHeader(header textproto.MIMEHeader, shown func(partName string) bool) (hidden textproto.MIMEHeader, hid, ok bool) {
	kind, params, err := mime.ParseMediaType(header.Get("Content-Disposition"))
	if err != nil {
		return nil, false, false
	}
	// Go reads a file name it can't decode, such as one in a charset it
	// doesn't know, as no file name, so the header's own words decide.
	named := strings.Contains(strings.ToLower(strings.Join(header.Values("Content-Disposition"), ";")), "filename")
	if !named || shown(params["name"]) {
		return header, false, true
	}
	for key := range params {
		// Any parameter whose name holds the word goes, so even a
		// careless reader that searches for filename= finds only REDACTED.
		if strings.Contains(key, "filename") {
			delete(params, key)
		}
	}
	params["filename"] = redactedName
	header.Set("Content-Disposition", mime.FormatMediaType(kind, params))
	return header, true, true
}

// endsWithClosingLine says whether the last delimiter line of a multipart
// body is its closing one: two dashes, the boundary, two dashes, then only
// spaces or tabs to the line's end. A delimiter line starts the body or
// follows a line feed, as Go's reader finds it.
func endsWithClosingLine(body []byte, boundary string) bool {
	delimiter := []byte("--" + boundary)
	at := bytes.LastIndex(body, append([]byte("\n"), delimiter...)) + 1
	if at == 0 && !bytes.HasPrefix(body, delimiter) {
		return false
	}
	line, _, _ := bytes.Cut(body[at+len(delimiter):], []byte("\n"))
	rest, closing := bytes.CutPrefix(line, []byte("--"))
	return closing && len(bytes.Trim(rest, " \t\r")) == 0
}
