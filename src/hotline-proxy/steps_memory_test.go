package proxy_test

import (
	"runtime"

	"github.com/cucumber/godog"
)

func memorySteps(sc *godog.ScenarioContext, w *world) {
	var perCall uint64
	sc.Step(`^an app sends (\d+) calls of "([A-Z]+) ([^"]+)"$`, func(n int, method, target string) error {
		// The first call starts the proxy and reads its config, which is
		// not part of a call's cost.
		if err := w.call(method, target); err != nil {
			return err
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range n {
			if err := w.call(method, target); err != nil {
				return err
			}
		}
		runtime.ReadMemStats(&after)
		perCall = (after.TotalAlloc - before.TotalAlloc) / uint64(n)
		return nil
	})
	sc.Step(`^the calls handed out less than (\d+) KB of memory each, on average$`, func(kb int) error {
		if perCall >= uint64(kb)<<10 {
			return failf("each call handed out %d KB on average, want less than %d KB", perCall>>10, kb)
		}
		return nil
	})
}
