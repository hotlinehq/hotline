package export_test

import (
	"bytes"
	"context"
	"io"
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
	sut := consoleLogsSUT{}

	It("keeps plain lines for a logger made with With, and shows its fields", func() {
		sut.forPlainLines()
		sut.Logger().With("endpoint", "otlp").Info("records sent", "count", 3)
		Expect(sut.Output()).To(MatchRegexp(`^\d\d:\d\d:\d\d\.\d{3} INFO  records sent  endpoint=otlp count=3\n$`))
	})

	It("keeps plain lines for a logger made with WithGroup, and names the fields after the group", func() {
		sut.forPlainLines()
		sut.Logger().WithGroup("queue").Warn("queue full", "size", 1000)
		Expect(sut.Output()).To(MatchRegexp(`^\d\d:\d\d:\d\d\.\d{3} WARN  queue full  queue\.size=1000\n$`))
	})

	It("quotes a value with spaces, so it cannot be read as more fields", func() {
		sut.forPlainLines()
		sut.Logger().Error("export failed", "error", "open /x y: no such file address=evil")
		Expect(sut.Output()).To(HaveSuffix(` error="open /x y: no such file address=evil"` + "\n"))
	})

	It("quotes an empty value", func() {
		sut.forPlainLines()
		sut.Logger().Info("started", "address", "")
		Expect(sut.Output()).To(HaveSuffix(` address=""` + "\n"))
	})

	It("escapes characters that could change the terminal inside a quoted value", func() {
		sut.forPlainLines()
		sut.Logger().Info("called", "path", "a b\u009b\u202e")
		Expect(sut.Output()).To(HaveSuffix(` path="a b\u009b\u202e"` + "\n"))
	})

	It("names a group's fields after the group, and an unnamed group's fields as they are", func() {
		sut.forPlainLines()
		sut.Logger().Info("called", slog.Group("request", "method", "GET"), slog.Group("", "size", 3))
		Expect(sut.Output()).To(HaveSuffix("  request.method=GET size=3\n"))
	})

	It("ignores a group with no name and a field with no name and no value", func() {
		sut.forPlainLines()
		h := sut.Logger().Handler().WithGroup("").WithAttrs([]slog.Attr{{}})
		slog.New(h).Info("started", "address", "x")
		Expect(sut.Output()).To(HaveSuffix("started  address=x\n"))
	})

	It("writes a reason after the message and a colon", func() {
		sut.forPlainLines()
		sut.Logger().Warn("config warning", "reason", "there is no listen.admin address")
		Expect(sut.Output()).To(MatchRegexp(`^\d\d:\d\d:\d\d\.\d{3} WARN  config warning: there is no listen\.admin address\n$`))
	})

	It("quotes a value that holds a character that could change the terminal", func() {
		sut.forPlainLines()
		sut.Logger().Info("called", "path", "a\u009bb")
		Expect(sut.Output()).To(HaveSuffix(` path="a\u009bb"` + "\n"))
	})

	DescribeTable("colours a level by the range it falls in",
		func(level slog.Level, want string) {
			sut.forColouredLines()
			sut.Logger().Log(context.Background(), level, "x")
			Expect(sut.Output()).To(ContainSubstring(want))
		},
		Entry("below debug", slog.LevelDebug-4, "\x1b[2mDEBUG-4\x1b[0m"),
		Entry("between debug and info", slog.LevelDebug+2, "\x1b[2mDEBUG+2\x1b[0m"),
		Entry("info", slog.LevelInfo, " INFO  x"),
		Entry("between info and warn", slog.LevelInfo+2, " INFO+2 x"),
		Entry("between warn and error", slog.LevelWarn+2, "\x1b[33mWARN+2\x1b[0m"),
		Entry("above error", slog.LevelError+2, "\x1b[31mERROR+2\x1b[0m"),
	)

	It("writes one line at a time when many goroutines log at once", func() {
		sut.forPlainLinesToAWriterThatNotesOverlaps()
		sut.LogFromManyGoroutinesAtOnce()
		Expect(sut.WritesOverlapped()).To(BeFalse())
	})
})

// consoleLogsSUT is a logger whose lines the console's log handler
// writes, never to a terminal, and what it wrote.
type consoleLogsSUT struct {
	out     *bytes.Buffer
	overlap *oneAtATime
	logger  *slog.Logger
}

// forPlainLines is a logger that never colours its lines, down to the
// lowest level.
func (s *consoleLogsSUT) forPlainLines() { s.forColor("never") }

// forColouredLines is a logger that always colours its lines.
func (s *consoleLogsSUT) forColouredLines() { s.forColor("always") }

func (s *consoleLogsSUT) forColor(color string) {
	s.out = &bytes.Buffer{}
	s.logger = s.consoleLogger(color, s.out, slog.NewJSONHandler(s.out, &slog.HandlerOptions{Level: slog.LevelDebug - 4}))
}

// forPlainLinesToAWriterThatNotesOverlaps is a logger that writes plain
// lines to a writer that notes when two writes overlap.
func (s *consoleLogsSUT) forPlainLinesToAWriterThatNotesOverlaps() {
	s.overlap = &oneAtATime{}
	s.logger = s.consoleLogger("never", s.overlap, slog.NewJSONHandler(s.overlap, nil))
}

func (*consoleLogsSUT) consoleLogger(color string, out io.Writer, next slog.Handler) *slog.Logger {
	console := export.Console{
		Out:       out,
		Terminal:  func() (int, bool) { return 0, false },
		LookupEnv: func(string) (string, bool) { return "", false },
	}
	return slog.New(export.ConsoleLogs(&config.ConsoleExporter{Color: color}, console, next))
}

// Logger is the logger to log with.
func (s *consoleLogsSUT) Logger() *slog.Logger { return s.logger }

// Output is what the logger wrote.
func (s *consoleLogsSUT) Output() string { return s.out.String() }

// LogFromManyGoroutinesAtOnce logs 20 lines from each of 8 goroutines
// running at once.
func (s *consoleLogsSUT) LogFromManyGoroutinesAtOnce() {
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				s.logger.Info("x", "a", 1)
			}
		})
	}
	wg.Wait()
}

// WritesOverlapped says two writes to the writer overlapped.
func (s *consoleLogsSUT) WritesOverlapped() bool { return s.overlap.overlapped.Load() }

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
