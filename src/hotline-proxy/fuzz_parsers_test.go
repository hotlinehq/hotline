//go:build fuzz

package proxy //nolint:testpackage // fuzzes inner parsers, by the fuzzing exception

// Fuzz tests straight at the parsers, a written exception to testing only
// through the proxy's ways in (docs/decisions/fuzzing.md). They are built
// only with the fuzz tag, so make test, CI and the coverage count never see
// them; the nightly workflow fuzz.yml runs them. An input they find gets a
// behaviour test with its fix. A parser that moves takes its fuzz test with
// it, or the test is deleted: it is no contract.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"slices"
	"testing"
	"time"

	"hotline-proxy/internal/spec"
)

// parseWait is the longest one parser may take over one input.
const parseWait = time.Second

func FuzzReadHello(f *testing.F) {
	f.Add([]byte{0x16, 3, 1, 0, 5, 1, 0, 0, 1, 0})
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		_, _ = readHello(bufio.NewReaderSize(bytes.NewReader(data), maxHello))
		if took := time.Since(start); took > parseWait {
			t.Fatalf("reading the hello took %v", took)
		}
	})
}

func FuzzSentID(f *testing.F) {
	f.Add("Basic dGFsb24tc2hvcC1hOng=")
	f.Add("basic Og==")
	f.Fuzz(func(t *testing.T, value string) {
		_ = sentID(map[string][]string{"Proxy-Authorization": {value}})
	})
}

// FuzzWireFrames checks that a WebSocket's frames, read as the wire's
// reader reads them, never hold more whole messages than the scanner on
// the bytes' way saw end: the reader takes each message's place from
// those the scanner saw.
func FuzzWireFrames(f *testing.F) {
	f.Add([]byte{0x81, 0x82, 1, 2, 3, 4, 'h', 'i'})
	f.Add([]byte{0x01, 0x01, 'a', 0x89, 0x00, 0x80, 0x01, 'b'})
	f.Add([]byte{0x80, 0x02, 'x', 'y', 0x81, 0x7e, 0x00, 0x01, 'z'})
	f.Fuzz(func(t *testing.T, data []byte) {
		var s frameScanner
		seen := 0
		s.feed(data, time.Time{}, func(time.Time) { seen++ })
		fr := &frameReader{in: bytes.NewReader(data), control: func(byte, []byte) {}}
		read := 0
		for {
			first, err := fr.message()
			if err != nil {
				break
			}
			if _, err := io.Copy(io.Discard, &messageBody{fr: fr, fin: first.fin}); err != nil {
				break
			}
			read++
		}
		if read > seen {
			t.Fatalf("the reader read %d messages, the scanner saw %d end", read, seen)
		}
	})
}

// FuzzSSEEvents reads made-up bytes as Server-Sent Events, whole and one
// byte at a time, and checks that both give the same events, each written
// as valid JSON, and never more bytes than were read.
func FuzzSSEEvents(f *testing.F) {
	f.Add([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
	f.Add([]byte("\xef\xbb\xbfid: 7\r\nretry: 3000\r\ndata: a\r\ndata: b\r\n\r\n: comment\rdata\r\r"))
	f.Add([]byte("\xef\xbbdata: x\n\nretry: +5\nid: a\x00b\nevent\ndata:\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		read := func(step int) []streamItem {
			var got []streamItem
			r := sseReader{delivered: func(e streamItem) { got = append(got, e) }}
			at := time.Unix(0, 0)
			for p := data; len(p) > 0; p = p[min(step, len(p)):] {
				r.pass(p[:min(step, len(p))], at)
			}
			return got
		}
		start := time.Now()
		whole, bytewise := read(len(data)+1), read(1)
		if took := time.Since(start); took > parseWait {
			t.Fatalf("reading the events took %v", took)
		}
		if len(whole) != len(bytewise) {
			t.Fatalf("read whole: %d events; one byte at a time: %d", len(whole), len(bytewise))
		}
		var size int64
		for i := range whole {
			if !bytes.Equal(whole[i].asJSON, bytewise[i].asJSON) || whole[i].size != bytewise[i].size {
				t.Fatalf("event %d read whole is %q, %d bytes; one byte at a time %q, %d bytes",
					i+1, whole[i].asJSON, whole[i].size, bytewise[i].asJSON, bytewise[i].size)
			}
			if whole[i].asJSON != nil && !json.Valid(whole[i].asJSON) {
				t.Fatalf("event %d is not valid JSON: %q", i+1, whole[i].asJSON)
			}
			size += whole[i].size
		}
		if size > int64(len(data)) {
			t.Fatalf("the events count %d bytes of %d", size, len(data))
		}
	})
}

// FuzzLineItems reads made-up bytes as JSON Lines or as a JSON sequence,
// whole and one byte at a time, and checks that both give the same stream
// items, that none holds the byte that ends it, and that they never count
// more bytes than were read.
func FuzzLineItems(f *testing.F) {
	f.Add([]byte("{\"id\":\"A-1\",\"total\":1}\r\n\n{\"id\":\"A-2\"}"), false)
	f.Add([]byte("\x1e{\"id\":\"A-1\",\n\"total\":1}\n\x1e\n\x1e{\"id\":\"A-2\"}"), true)
	f.Add([]byte("junk\x1e  \x1e{}\n"), true)
	f.Fuzz(func(t *testing.T, data []byte, seq bool) {
		ends := byte('\n')
		if seq {
			ends = recordSeparator
		}
		read := func(step int) []streamItem {
			var got []streamItem
			delivered := func(e streamItem) { got = append(got, e) }
			var r streamItemReader = &jsonLinesReader{current: openStreamItem{delivered: delivered}}
			if seq {
				r = &jsonSeqReader{current: openStreamItem{delivered: delivered}}
			}
			at := time.Unix(0, 0)
			for p := data; len(p) > 0; p = p[min(step, len(p)):] {
				r.pass(p[:min(step, len(p))], at)
			}
			r.end()
			return got
		}
		start := time.Now()
		whole, bytewise := read(len(data)+1), read(1)
		if took := time.Since(start); took > parseWait {
			t.Fatalf("reading the stream items took %v", took)
		}
		if len(whole) != len(bytewise) {
			t.Fatalf("read whole: %d stream items; one byte at a time: %d", len(whole), len(bytewise))
		}
		var size int64
		for i := range whole {
			if !bytes.Equal(whole[i].asJSON, bytewise[i].asJSON) || whole[i].size != bytewise[i].size {
				t.Fatalf("stream item %d read whole is %q, %d bytes; one byte at a time %q, %d bytes",
					i+1, whole[i].asJSON, whole[i].size, bytewise[i].asJSON, bytewise[i].size)
			}
			if bytes.IndexByte(whole[i].asJSON, ends) >= 0 {
				t.Fatalf("stream item %d holds the byte that ends it: %q", i+1, whole[i].asJSON)
			}
			size += whole[i].size
		}
		if size > int64(len(data)) {
			t.Fatalf("the stream items count %d bytes of %d", size, len(data))
		}
	})
}

// FuzzFormParts reads made-up bytes as a multipart/form-data body, whole
// and one byte at a time, and checks that both give the same parts, that
// they never count more bytes than were read, and that a body Go's own
// multipart reader reads whole gives the same text parts, when its lines end
// as RFC 2046 says.
func FuzzFormParts(f *testing.F) {
	f.Add([]byte("--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nwhisper-1\r\n--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\nContent-Type: audio/mpeg\r\n\r\n\x00\x01\r\n-\r\n--b--\r\n"))
	f.Add([]byte("preamble\r\n--b \t\r\n\r\n\r\n--b\r\nContent-Disposition: form-data; name=x\r\n\r\n\r\n--bb\r\n--b--"))
	f.Fuzz(func(t *testing.T, data []byte) {
		read := func(step int) *partReader {
			r := newPartReader("multipart/form-data; boundary=b", func(name string) bool { return name == "file" }, 64, &bodyPool{})
			for p := data; len(p) > 0; p = p[min(step, len(p)):] {
				r.add(p[:min(step, len(p))])
			}
			r.hand()
			return r
		}
		start := time.Now()
		whole, bytewise := read(len(data)+1), read(1)
		if took := time.Since(start); took > parseWait {
			t.Fatalf("reading the parts took %v", took)
		}
		all := func(r *partReader) []spec.FormPart { return append(slices.Clone(r.text), r.files...) }
		wholeParts, bytewiseParts := all(whole), all(bytewise)
		if whole.state != bytewise.state || len(wholeParts) != len(bytewiseParts) {
			t.Fatalf("read whole: state %d, %d parts; one byte at a time: state %d, %d parts", whole.state, len(wholeParts), bytewise.state, len(bytewiseParts))
		}
		var size int64
		for i := range wholeParts {
			w, b := wholeParts[i], bytewiseParts[i]
			if w.Name != b.Name || w.Size != b.Size || !bytes.Equal(w.Value, b.Value) || w.TooLarge != b.TooLarge {
				t.Fatalf("part %d read whole is %+v; one byte at a time %+v", i+1, w, b)
			}
			size += w.Size
		}
		if size > int64(len(data)) {
			t.Fatalf("the parts count %d bytes of %d", size, len(data))
		}
		if whole.state != afterLastPart || bytes.Count(data, []byte("\n")) != bytes.Count(data, []byte("\r\n")) {
			// Go's reader also takes a line feed alone as a line end; RFC
			// 2046, and ours, take only a carriage return and line feed.
			return
		}
		std := multipart.NewReader(bytes.NewReader(data), "b")
		text := 0
		for i := 0; ; i++ {
			p, err := std.NextRawPart()
			if err != nil {
				return
			}
			got, _ := io.ReadAll(p)
			if i >= len(wholeParts) {
				// Ours stops at the first boundary followed by two
				// dashes, as RFC 2046 says; Go's reader also wants a line
				// end after them, and reads on when there is none.
				return
			}
			if p.FormName() == "file" {
				continue
			}
			if own := whole.text[text]; !own.TooLarge && !bytes.Equal(got, own.Value) {
				t.Fatalf("part %d is %q to Go's reader, %q to ours", i+1, got, own.Value)
			}
			text++
		}
	})
}
