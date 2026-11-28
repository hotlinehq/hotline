package proxy_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// The header of a queue file: its magic, version and first item's number.
const queueFileHeader = 13

func diskQueueSteps(sc *godog.ScenarioContext, sut *otlpSUT) {
	queueFolder := func() string { return filepath.Join(sut.proxy.dir, "queue") }
	stripeCall := func(i int) error {
		started := time.Now()
		if err := sut.app.call("GET", fmt.Sprintf("https://api.stripe.com/v1/payment_intents/pi_%d", i)); err != nil {
			return err
		}
		sut.app.appTook = max(sut.app.appTook, time.Since(started))
		// A record is queued just after its response has gone back, so give
		// it time to be queued before the next call.
		pause(20 * time.Millisecond)
		return nil
	}
	calls := 0
	sc.Step(`^an exporter with queueSize (\d+) and a disk queue(, whose collector is busy)?$`, func(size int, busy string) error {
		sut.exporterExtra = []string{fmt.Sprintf("      queueSize: %d\n      queueFolder: queue\n      queueDisk: 64MiB\n", size)}
		sut.receivers[0].setBusy(busy != "")
		// Each send gives up after its first try, so the test sees the
		// sender's own waits between tries.
		sut.exportTimeout = 300 * time.Millisecond
		sut.proxy.stopTime = 2 * time.Second
		return nil
	})
	sc.Step(`^the exporter keeps a disk queue(, and its collector is busy)?$`, func(busy string) error {
		sut.exporterExtra = []string{"      queueFolder: queue\n      queueDisk: 64MiB\n"}
		sut.receivers[0].setBusy(busy != "")
		sut.exportTimeout = 300 * time.Millisecond
		sut.proxy.stopTime = 2 * time.Second
		return nil
	})
	sc.Step(`^the proxy warns that the export queue was full and new items go to the disk queue$`, func() error {
		sut.proxy.settle()
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "go to the disk queue") {
				return nil
			}
		}
		return failf("no warning that the disk queue is in use; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^(\d+) calls end one after another$`, func(n int) error {
		for range n {
			calls++
			if err := stripeCall(calls); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the collector is back, and 1 more call ends at once$`, func() error {
		sut.receivers[0].setBusy(false)
		calls++
		return stripeCall(calls)
	})
	sc.Step(`^the proxy stops cleanly$`, func() error {
		sut.proxy.settle()
		return nil
	})
	sc.Step(`^the proxy stops, and starts again with the collector back$`, func() error {
		sut.proxy.settle()
		sut.receivers[0].setBusy(false)
		return sut.proxy.start()
	})
	sc.Step(`^the proxy starts again with the collector back$`, func() error {
		sut.receivers[0].setBusy(false)
		return sut.proxy.start()
	})
	sc.Step(`^the last item on disk is damaged$`, func() error {
		records, err := os.OpenRoot(filepath.Join(queueFolder(), "records"))
		if err != nil {
			return err
		}
		defer func() { _ = records.Close() }()
		files, _ := fs.Glob(records.FS(), "*.queue")
		slices.Sort(files)
		for i := len(files) - 1; i >= 0; i-- {
			data, err := records.ReadFile(files[i])
			if err != nil {
				return err
			}
			if len(data) > queueFileHeader {
				data[len(data)-1] ^= 0xff
				return records.WriteFile(files[i], data, 0o600)
			}
		}
		return failf("no item on disk to damage")
	})
	sc.Step(`^the collector gets (\d+) records$`, func(n int) error {
		if got := len(sut.records()); got != n {
			return failf("the collector got %d records, want %d", got, n)
		}
		return nil
	})
	wantOrder := func(first, last int) error {
		if err := sut.waitForRecords(last - first + 1); err != nil {
			return err
		}
		var got, want []string
		for _, r := range sut.records() {
			got = append(got, sprint(r.attrs["url.full"]))
		}
		for i := first; i <= last; i++ {
			want = append(want, fmt.Sprintf("https://api.stripe.com/v1/payment_intents/pi_%d", i))
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return failf("the collector got %v, want %v", got, want)
		}
		return nil
	}
	sc.Step(`^the collector gets records (\d+) to (\d+) in the order of the calls$`, wantOrder)
	sc.Step(`^every call's spans$`, func() error {
		traces := map[string]bool{}
		for _, s := range sut.receivers[0].spans() {
			traces[s.traceID] = true
		}
		for _, r := range sut.records() {
			if !traces[r.traceID] {
				return failf("no span of the call to %v arrived", r.attrs["url.full"])
			}
		}
		return nil
	})
	sc.Step(`^the disk queue holds no record$`, func() error {
		files, _ := filepath.Glob(filepath.Join(queueFolder(), "records", "*.queue"))
		if len(files) == 0 {
			return failf("the disk queue has no queue file")
		}
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil {
				return err
			}
			if info.Size() != queueFileHeader {
				return failf("%s holds %d bytes, want only its header", f, info.Size())
			}
		}
		return nil
	})
	sc.Step(`^the proxy warns that 1 item on disk could not be read$`, func() error {
		sut.proxy.settle()
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "could not be read") && strings.Contains(line, "dropped=1") {
				return nil
			}
		}
		return failf("no warning about the record that could not be read; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^the queue folder and its files are open to the proxy's user only$`, func() error {
		sut.proxy.settle()
		return filepath.Walk(queueFolder(), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			want := os.FileMode(0o600)
			if info.IsDir() {
				want = 0o700
			}
			if info.Mode().Perm() != want {
				return failf("%s is open as %v, want %v", path, info.Mode().Perm(), want)
			}
			return nil
		})
	})
}
