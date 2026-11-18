//go:build fuzz

package export //nolint:testpackage // fuzzes inner parsers, by the fuzzing exception

// A fuzz test straight at the reader of record lines, which
// hotline-proxy verify uses, a written exception to testing only through
// the proxy's ways in (docs/decisions/fuzzing.md). It is built only with the
// fuzz tag and runs in the nightly workflow fuzz.yml.

import (
	"os"
	"testing"

	"hotline-proxy/internal/fuzzlog"
)

// TestMain keeps each fuzz worker's output in a file, so a crash says why
// (internal/fuzzlog). It does nothing outside a fuzz worker.
func TestMain(m *testing.M) {
	fuzzlog.Capture()
	os.Exit(m.Run())
}

func FuzzReadLine(f *testing.F) {
	f.Add([]byte(`{"time":"2026-11-18T07:00:00Z","event":"hotline.request","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7","attributes":{"hotline.trace.spans":[{"id":"00f067aa0ba902b7","name":"call","start":"2026-11-18T07:00:00Z","end":"2026-11-18T07:00:01Z","attributes":{}}]}}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ReadLine(data)
	})
}
