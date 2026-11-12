package export_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
)

// The program's own logs on the console are plain lines. The handler is a
// slog.Handler, a small and stable interface with many cases of its own,
// so it gets a solitary test (the hotline-go-proxy skill allows this).
var _ = Describe("Plain log lines", func() {
	var out *bytes.Buffer

	logger := func(color string) *slog.Logger {
		console := export.Console{
			Out:       out,
			Terminal:  func() (int, bool) { return 0, false },
			LookupEnv: func(string) (string, bool) { return "", false },
		}
		next := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug - 4})
		return slog.New(export.ConsoleLogs(&config.ConsoleExporter{Color: color}, console, next))
	}

	BeforeEach(func() { out = &bytes.Buffer{} })

	It("keeps plain lines for a logger made with With, and shows its fields", func() {
		logger("never").With("endpoint", "otlp").Info("records sent", "count", 3)
		Expect(out.String()).To(MatchRegexp(`^\d\d:\d\d:\d\d\.\d{3} INFO  records sent  endpoint=otlp count=3\n$`))
	})

	It("keeps plain lines for a logger made with WithGroup, and names the fields after the group", func() {
		logger("never").WithGroup("queue").Warn("queue full", "size", 1000)
		Expect(out.String()).To(MatchRegexp(`^\d\d:\d\d:\d\d\.\d{3} WARN  queue full  queue\.size=1000\n$`))
	})

	It("quotes a value with spaces, so it cannot be read as more fields", func() {
		logger("never").Error("export failed", "error", "open /x y: no such file address=evil")
		Expect(out.String()).To(HaveSuffix(` error="open /x y: no such file address=evil"` + "\n"))
	})

	It("quotes an empty value", func() {
		logger("never").Info("started", "address", "")
		Expect(out.String()).To(HaveSuffix(` address=""` + "\n"))
	})

	It("escapes characters that could change the terminal inside a quoted value", func() {
		logger("never").Info("called", "path", "a b\u009b\u202e")
		Expect(out.String()).To(HaveSuffix(` path="a b\u009b\u202e"` + "\n"))
	})

	DescribeTable("colours a level by the range it falls in",
		func(level slog.Level, want string) {
			logger("always").Log(context.Background(), level, "x")
			Expect(out.String()).To(ContainSubstring(want))
		},
		Entry("below debug", slog.LevelDebug-4, "\x1b[2mDEBUG-4\x1b[0m"),
		Entry("between debug and info", slog.LevelDebug+2, "\x1b[2mDEBUG+2\x1b[0m"),
		Entry("info", slog.LevelInfo, " INFO  x"),
		Entry("between info and warn", slog.LevelInfo+2, " INFO+2 x"),
		Entry("between warn and error", slog.LevelWarn+2, "\x1b[33mWARN+2\x1b[0m"),
		Entry("above error", slog.LevelError+2, "\x1b[31mERROR+2\x1b[0m"),
	)

	It("writes one line at a time when many goroutines log at once", func() {
		w := &oneAtATime{}
		console := export.Console{Out: w, Terminal: func() (int, bool) { return 0, false }, LookupEnv: func(string) (string, bool) { return "", false }}
		l := slog.New(export.ConsoleLogs(&config.ConsoleExporter{Color: "never"}, console, slog.NewJSONHandler(w, nil)))
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 20 {
					l.Info("x", "a", 1)
				}
			})
		}
		wg.Wait()
		Expect(w.overlapped.Load()).To(BeFalse())
	})
})

// oneAtATime is a writer that notes when two writes overlap.
type oneAtATime struct {
	busy       atomic.Bool
	overlapped atomic.Bool
}

func (w *oneAtATime) Write(p []byte) (int, error) {
	if !w.busy.CompareAndSwap(false, true) {
		w.overlapped.Store(true)
		return len(p), nil
	}
	time.Sleep(10 * time.Microsecond)
	w.busy.Store(false)
	return len(p), nil
}
