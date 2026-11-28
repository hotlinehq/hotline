package export_test

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/metric/noop"

	"hotline-proxy/internal/export"
)

var _ = Describe("export queue", func() {
	sut := queueSUT{}

	It("sends what waits in the order it came", func() {
		sut.forQueue()
		sut.AddGroup("a", "b", "c")
		sut.SendWaiting()
		Expect(sut.SentItems()).To(Equal([]string{"a", "b", "c"}))
	})

	It("keeps the order when the items wrap around the end of the ring", func() {
		sut.forQueue()
		sut.AddGroup("a", "b", "c")
		sut.SendWaiting()
		sut.AddGroup("d", "e", "f", "g")
		sut.SendWaiting()
		Expect(sut.SentItems()).To(Equal([]string{"a", "b", "c", "d", "e", "f", "g"}))
	})

	It("refuses the newest items when they do not all fit", func() {
		sut.forQueue()
		sut.AddGroup("a", "b", "c")
		sut.AddGroup("d", "e")
		sut.AddGroup("f")
		sut.SendWaiting()
		Expect(sut.SentItems()).To(Equal([]string{"a", "b", "c", "f"}))
	})

	It("says what it refused, in items and in groups, when it stops", func() {
		sut.forQueue()
		sut.AddGroup("a", "b", "c")
		sut.AddGroup("d", "e")
		sut.AddGroup("f", "g")
		sut.Stop()
		Expect(sut.Logs()).To(ContainSubstring("dropped=4 calls=2"))
	})

	It("refuses a group larger than the whole queue", func() {
		sut.forQueue()
		sut.AddGroup("a", "b", "c", "d", "e")
		sut.Stop()
		Expect(sut.SentItems()).To(BeEmpty())
		Expect(sut.Logs()).To(ContainSubstring("dropped=5 calls=1"))
	})

	It("sends at most 512 items at a time", func() {
		sut.forQueueHolding(1000)
		sut.AddGroupOf(600)
		sut.SendWaiting()
		Expect(sut.BatchSizes()).To(Equal([]int{512, 88}))
		Expect(sut.SentItems()).To(Equal(sut.MadeUpItems(600)))
	})
})

// queueSUT is one export queue of text items, a fake sender that keeps
// every batch it is given, and the log the queue warns in.
type queueSUT struct {
	queue   *export.Queue
	stopped bool
	logs    *lockedLog

	mu      sync.Mutex
	batches [][]string
}

// forQueue is an empty queue that holds 4 items.
func (s *queueSUT) forQueue() { s.forQueueHolding(4) }

// forQueueHolding is an empty queue that holds at most size items. It
// sends only when the test says so, and stops when the test ends.
func (s *queueSUT) forQueueHolding(size int) {
	counter, err := noop.NewMeterProvider().Meter("test").Int64Counter("test")
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.batches, s.stopped, s.logs = nil, false, &lockedLog{}
	s.queue = export.NewQueue(export.QueueSetup{
		ItemName:     "spans",
		ExporterName: "http://collector.test",
		Size:         size,
		Interval:     time.Hour,
		Logger:       slog.New(slog.NewTextHandler(s.logs, nil)),
		Counts:       export.QueueCounts{Sent: counter, Dropped: counter},
		ExporterUp:   &atomic.Int64{},
		Send:         s.recordBatch,
	})
	DeferCleanup(s.Close)
}

// Close stops the queue, if the test did not already.
func (s *queueSUT) Close() {
	if !s.stopped {
		s.Stop()
	}
}

// AddGroup adds items as one group, as one call's spans are.
func (s *queueSUT) AddGroup(items ...string) {
	group := make([][]byte, len(items))
	for i, item := range items {
		group[i] = []byte(item)
	}
	s.queue.Add(group...)
}

// AddGroupOf adds n made-up items as one group.
func (s *queueSUT) AddGroupOf(n int) { s.AddGroup(s.MadeUpItems(n)...) }

// MadeUpItems are the n items AddGroupOf adds: "0", "1", and on.
func (s *queueSUT) MadeUpItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = strconv.Itoa(i)
	}
	return items
}

// SendWaiting sends everything that waits, and fails the test if a send
// fails.
func (s *queueSUT) SendWaiting() {
	Expect(s.queue.ForceFlush(context.Background())).WithOffset(1).To(Succeed())
}

// Stop sends what waits, warns about what was refused, and stops the
// queue.
func (s *queueSUT) Stop() {
	s.stopped = true
	Expect(s.queue.Shutdown(context.Background())).WithOffset(1).To(Succeed())
}

// SentItems are every item sent, in the order they were sent.
func (s *queueSUT) SentItems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []string
	for _, b := range s.batches {
		all = append(all, b...)
	}
	return all
}

// BatchSizes are how many items each send held.
func (s *queueSUT) BatchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sizes []int
	for _, b := range s.batches {
		sizes = append(sizes, len(b))
	}
	return sizes
}

// Logs is what the queue logged so far.
func (s *queueSUT) Logs() string { return s.logs.String() }

// recordBatch is the fake sender: it keeps a copy of the batch.
func (s *queueSUT) recordBatch(_ context.Context, batch [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	texts := make([]string, len(batch))
	for i, item := range batch {
		texts[i] = string(item)
	}
	s.batches = append(s.batches, texts)
	return nil
}

// lockedLog is a log the queue's sender may write to while the test reads
// it.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
