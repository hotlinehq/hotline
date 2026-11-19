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
