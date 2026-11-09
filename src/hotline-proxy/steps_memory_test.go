package proxy_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
	"golang.org/x/sync/errgroup"
)

func memorySteps(sc *godog.ScenarioContext, w *world) {
	var perCall uint64
	// measure makes one call, which starts the proxy and reads its config,
	// not part of a call's cost, then n more, and keeps the memory the
	// whole test run handed out for each of those, on average.
	measure := func(n uint64, call func() error) error {
		if err := call(); err != nil {
			return err
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range n {
			if err := call(); err != nil {
				return err
			}
		}
		runtime.ReadMemStats(&after)
		perCall = (after.TotalAlloc - before.TotalAlloc) / n
		return nil
	}
	sc.Step(`^an app sends (\d+) calls of "([A-Z]+) ([^"]+)"$`, func(n uint64, method, target string) error {
		return measure(n, func() error { return w.call(method, target) })
	})
	sc.Step(`^the vendor answers 200 with a (\d+) MiB pdf body$`, func(mib int) error {
		body := []byte(strings.Repeat("%PDF", mib<<18))
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", "application/pdf")
			rw.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = rw.Write(body)
		})
		return nil
	})
	sc.Step(`^the app sends GET (\S+) (\d+) times$`, func(path string, n uint64) error {
		return measure(n, func() error { return w.shopCall("GET", path, true, "", "", "") })
	})
	sc.Step(`^apps send (\d+) orders at once, each of a different size$`, func(n int) error {
		if err := w.start(); err != nil {
			return err
		}
		proxy := &url.URL{Scheme: "http", Host: w.addr}
		if w.appID != "" {
			proxy.User = url.UserPassword(w.appID, "x")
		}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
		defer client.CloseIdleConnections()
		var sent errgroup.Group
		sent.SetLimit(8)
		for i := 1; i <= n; i++ {
			sent.Go(func() error {
				// Sizes from 1 KB to about 400 KB, so the bodies need
				// buffers of every size from 16 KB to 512 KB. Every other
				// body has no Content-Length and comes in chunks.
				body := fmt.Sprintf(`{"item":"A%d-%s","quantity":%d}`, i, strings.Repeat("x", i*i*250), i)
				var reader io.Reader = strings.NewReader(body)
				if i%2 == 0 {
					reader = io.MultiReader(reader)
				}
				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://api.shop.test/orders", reader)
				if err != nil {
					return err
				}
				req.Header.Set("Authorization", "Bearer t0k3n")
				req.Header.Set("Idempotency-Key", "k"+strconv.Itoa(i))
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					return err
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				return resp.Body.Close()
			})
		}
		return sent.Wait()
	})
	sc.Step(`^each order's record holds its own item and quantity, and its check passed$`, func() error {
		records := w.records()
		if len(records) != 40 {
			return failf("%d records arrived, want 40", len(records))
		}
		for _, r := range records {
			quantity := fmt.Sprint(r.attrs["shop.order.quantity"])
			item, _ := r.attrs["shop.order.item"].(string)
			if !strings.HasPrefix(item, "A"+quantity+"-") {
				return failf("a record holds item %.20q with quantity %s, from another order", item, quantity)
			}
			if r.attrs["hotline.schema.status"] != "valid" {
				return failf("the check of order %s says %v, want valid", quantity, r.attrs["hotline.schema.status"])
			}
		}
		return nil
	})
	sc.Step(`^the calls handed out less than (\d+) KB of memory each, on average$`, func(kb uint64) error {
		if raceEnabled {
			// The race detector makes every allocation larger, so the
			// limit only means something without it.
			return nil
		}
		if perCall >= kb<<10 {
			return failf("each call handed out %d KB on average, want less than %d KB", perCall>>10, kb)
		}
		return nil
	})
}
