package export_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/export"
)

// gotBodies is a collector that keeps every body it was sent, unzipped,
// and answers 400 to a body it cannot unzip.
type gotBodies struct {
	mu     sync.Mutex
	bodies map[string]int
	broken int
}

func (g *gotBodies) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	zr, err := gzip.NewReader(r.Body)
	var body []byte
	if err == nil {
		body, err = io.ReadAll(zr)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.broken++
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.bodies[string(body)]++
}

var _ = Describe("sending over HTTP", func() {
	// A destination sends records from two places at once: its record
	// queue and the ledger's batch line. Sharing one gzip writer between
	// them mixed their bodies, and could crash the proxy (fuzz run of
	// 2026-11-18).
	It("keeps the bodies of two senders apart when they send at the same time", func() {
		got := &gotBodies{bodies: map[string]int{}}
		collector := httptest.NewServer(got)
		defer collector.Close()
		transport := export.NewTestTransport(collector.URL)
		const sends = 200
		var wg sync.WaitGroup
		for _, name := range []string{"queue", "batch"} {
			send := transport.Sender()
			wg.Go(func() {
				for i := range sends {
					body := bytes.Repeat([]byte(fmt.Sprintf("%s-%d;", name, i)), 50)
					_ = send(context.Background(), body)
				}
			})
		}
		wg.Wait()

		got.mu.Lock()
		defer got.mu.Unlock()
		Expect(got.broken).To(BeZero(), "bodies the collector could not unzip")
		for _, name := range []string{"queue", "batch"} {
			for i := range sends {
				body := bytes.Repeat([]byte(fmt.Sprintf("%s-%d;", name, i)), 50)
				Expect(got.bodies).To(HaveKeyWithValue(string(body), 1), "the body of %s send %d", name, i)
			}
		}
	})
})
