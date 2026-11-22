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
	"testing"
	"time"
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
		var s scanner
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
		read := func(step int) []sseEvent {
			var got []sseEvent
			r := sseReader{delivered: func(e sseEvent) { got = append(got, e) }}
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
			if !bytes.Equal(whole[i].item, bytewise[i].item) || whole[i].size != bytewise[i].size {
				t.Fatalf("event %d read whole is %q, %d bytes; one byte at a time %q, %d bytes",
					i+1, whole[i].item, whole[i].size, bytewise[i].item, bytewise[i].size)
			}
			if whole[i].item != nil && !json.Valid(whole[i].item) {
				t.Fatalf("event %d is not valid JSON: %q", i+1, whole[i].item)
			}
			size += whole[i].size
		}
		if size > int64(len(data)) {
			t.Fatalf("the events count %d bytes of %d", size, len(data))
		}
	})
}
