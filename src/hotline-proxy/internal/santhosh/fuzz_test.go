//go:build fuzz

package santhosh //nolint:testpackage // fuzzes inner parsers, by the fuzzing exception

// A fuzz test straight at the JSON reader, a written exception to testing
// only through the proxy's ways in (docs/decisions/fuzzing.md). It is built
// only with the fuzz tag and runs in the nightly workflow fuzz.yml.

import (
	"testing"
	"time"
)

func FuzzDecodeJSON(f *testing.F) {
	f.Add([]byte(`{"item":"A1","quantity":1,"lines":[{"sku":"s"}]}`))
	f.Add([]byte(`[1,2.5e3,null,true,"x"]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		_, _ = decodeJSON(data)
		if took := time.Since(start); took > time.Second {
			t.Fatalf("reading the JSON took %v", took)
		}
	})
}
