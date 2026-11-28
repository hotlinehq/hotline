package export

import (
	"bytes"
	"context"
	"net/url"
	"time"

	"hotline-proxy/internal/config"
)

// What the tests in export_test may reach: a call's line, and a transport
// to one collector.

// LineOf is a call's line as a file exporter writes it, with content:
// full and the proxy's own resource.
func LineOf(c Call) []byte {
	buf := &bytes.Buffer{}
	appendLine(buf, describe(Service{}, nil).Attributes(), c, true)
	return buf.Bytes()
}

// TestTransport sends gzipped records to one collector over HTTP, like a
// exporter does.
type TestTransport struct{ t *httpTransport }

// NewTestTransport sends to the collector at rawURL, with gzip on.
func NewTestTransport(rawURL string) TestTransport {
	u, _ := url.Parse(rawURL)
	return TestTransport{newHTTPTransport(config.Exporter{URL: u, Gzip: true}, nil, "test")}
}

// Sender is one more sender of records over the transport, like the record
// queue or the ledger's batch line of an exporter.
func (t TestTransport) Sender() func(ctx context.Context, body []byte) error {
	return t.SenderWithin(time.Second)
}

// SenderWithin is a sender whose each send may take up to timeout.
func (t TestTransport) SenderWithin(timeout time.Duration) func(ctx context.Context, body []byte) error {
	z := t.t.newZipper()
	return func(ctx context.Context, body []byte) error {
		return sendWithRetry(ctx, t.t, records, z, body, timeout)
	}
}
