package export

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/metric/noop"
)

// sentBatches is a sender that keeps every batch it was given.
type sentBatches struct {
	mu      sync.Mutex
	batches [][]string
}

func (s *sentBatches) export(_ context.Context, batch []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, append([]string(nil), batch...))
	return nil
}

func (s *sentBatches) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []string
	for _, b := range s.batches {
		all = append(all, b...)
	}
	return all
}

func (s *sentBatches) sizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sizes []int
	for _, b := range s.batches {
		sizes = append(sizes, len(b))
	}
	return sizes
}

var _ = Describe("queue", func() {
	var (
		sent *sentBatches
		q    *queue[string]
	)
	start := func(size int) {
		sent = &sentBatches{}
		counter, _ := noop.NewMeterProvider().Meter("test").Int64Counter("test")
		q = newQueue("spans", sent.export, func(context.Context) error { return nil }, queueSetup{
			size: size, interval: time.Hour, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			sent: counter, dropped: counter, up: &atomic.Int64{},
		})
	}
	AfterEach(func() {
		Expect(q.Shutdown(context.Background())).To(Succeed())
	})

	It("sends what waits in the order it came", func() {
		start(4)
		q.add("a", "b", "c")
		Expect(q.ForceFlush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c"}))
	})

	It("keeps the order when the items wrap around the end of the ring", func() {
		start(4)
		q.add("a", "b", "c")
		Expect(q.ForceFlush(context.Background())).To(Succeed())
		q.add("d", "e", "f", "g")
		Expect(q.ForceFlush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c", "d", "e", "f", "g"}))
	})

	It("refuses the newest items when they do not all fit", func() {
		start(4)
		q.add("a", "b", "c")
		q.add("d", "e")
		q.add("f")
		waiting, _ := q.fill()
		Expect(waiting).To(Equal(4))
		Expect(q.ForceFlush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c", "f"}))
	})

	It("counts what it refused, in items and in groups", func() {
		start(4)
		q.add("a", "b", "c")
		q.add("d", "e")
		q.add("f", "g")
		Expect(q.crowded.count).To(Equal(4))
		Expect(q.crowded.groups).To(Equal(2))
	})

	It("refuses a group larger than the whole queue", func() {
		start(4)
		q.add("a", "b", "c", "d", "e")
		waiting, _ := q.fill()
		Expect(waiting).To(Equal(0))
		Expect(q.crowded.count).To(Equal(5))
	})

	It("sends at most one batch's worth at a time", func() {
		start(1000)
		items := make([]string, 600)
		for i := range items {
			items[i] = string(rune('a' + i%26))
		}
		q.add(items...)
		Expect(q.ForceFlush(context.Background())).To(Succeed())
		Expect(sent.sizes()).To(Equal([]int{batchSize, 600 - batchSize}))
		Expect(sent.all()).To(Equal(items))
	})
})
