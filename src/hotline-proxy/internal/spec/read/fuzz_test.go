//go:build fuzz

package read //nolint:testpackage // fuzzes inner parsers, by the fuzzing exception

// Fuzz tests straight at the body readers, a written exception to testing
// only through the proxy's ways in (docs/decisions/fuzzing.md). They are
// built only with the fuzz tag and run in the nightly workflow fuzz.yml.

import (
	"os"
	"testing"
	"time"

	"hotline-proxy/internal/fuzzlog"
)

// TestMain keeps each fuzz worker's output in a file, so a crash says why
// (internal/fuzzlog). It does nothing outside a fuzz worker.
func TestMain(m *testing.M) {
	fuzzlog.Capture()
	os.Exit(m.Run())
}

// readWait is the longest one body may take to read.
const readWait = time.Second

func FuzzForm(f *testing.F) {
	f.Add([]byte("amount=5&currency=eur&metadata[order]=6735&items[0][price]=p1&items[0][quantity]=2"))
	f.Add([]byte("0=x&1=y"))
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		_, _ = Form(data)
		if took := time.Since(start); took > readWait {
			t.Fatalf("reading the form took %v", took)
		}
	})
}

func FuzzXML(f *testing.F) {
	shape := &XMLShape{Kind: Object, Props: []XMLProp{
		{Key: "Name", Name: "Name", Shape: &XMLShape{Kind: Text}},
		{Key: "id", Name: "id", Attribute: true, Shape: &XMLShape{Kind: Text}},
		{Key: "Contents", Name: "Contents", Shape: &XMLShape{Kind: Array, Item: &XMLShape{Kind: Any}}},
	}}
	body := &XMLBody{Root: "ListBucketResult", Shape: shape}
	f.Add([]byte(`<ListBucketResult id="1"><Name>a</Name><Contents><Key>k</Key></Contents><Other><a/></Other></ListBucketResult>`))
	f.Add([]byte(`<?xml version="1.0"?><ListBucketResult/>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		_, _ = body.Read(data)
		_, _ = (&XMLBody{Shape: &XMLShape{Kind: Any}}).Read(data)
		_, _ = (&XMLBody{}).Read(data)
		if took := time.Since(start); took > readWait {
			t.Fatalf("reading the XML took %v", took)
		}
	})
}
