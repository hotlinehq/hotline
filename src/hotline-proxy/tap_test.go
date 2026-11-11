package proxy_test

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	proxy "hotline-proxy"
)

var _ = Describe("A body the proxy passes on", func() {
	// A WebSocket's two copies run at once: one reads the vendor's side to
	// its end while the call ending closes it. The record's end must run
	// once, and the race detector must find no unguarded access.
	It("ends once when it is read to the end and closed at the same time", func() {
		for range 200 {
			var ended atomic.Int32
			body := proxy.NewTap(io.NopCloser(strings.NewReader("ping")), func() { ended.Add(1) })

			var both sync.WaitGroup
			both.Go(func() { _, _ = io.Copy(io.Discard, body) })
			both.Go(func() { _ = body.Close() })
			both.Wait()

			Expect(ended.Load()).To(Equal(int32(1)))
		}
	})
})
