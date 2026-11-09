package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// BenchmarkCall is one call through the whole proxy: an app on a kept-alive
// connection sends a POST to a fake vendor over the forward proxy ingress,
// and reads the reply. App, proxy and vendor run in this one process, so the
// numbers per call are for all three together; a change in the proxy shows
// as the difference between two runs.
func BenchmarkCall(b *testing.B) {
	w, err := newWorld()
	if err != nil {
		b.Fatal(err)
	}
	defer w.close()
	if err := w.start(); err != nil {
		b.Fatal(err)
	}
	w.vendor("api.talon.one")
	proxyURL, _ := url.Parse("http://" + w.addr)
	app := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableCompression: true}}
	body := strings.Repeat("x", 1024)
	send := func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://api.talon.one/v2/customer_sessions/sess-1", strings.NewReader(body))
		req.Header.Set("Proxy-Authorization", "Basic dGFsb24tc2hvcC1hOng=")
		req.Header.Set("Content-Type", "application/json")
		for i := range 10 {
			req.Header.Set("X-App-"+string(rune('a'+i)), "some value of twenty")
		}
		res, err := app.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}
	send()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		send()
	}
}
