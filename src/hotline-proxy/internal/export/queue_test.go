package export_test

import (
	"context"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/export"
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
		q    export.TestQueue
	)
	start := func(size int) {
		sent = &sentBatches{}
		q = export.NewTestQueue(size, sent.export)
	}
	AfterEach(func() {
		Expect(q.Shutdown(context.Background())).To(Succeed())
	})

	It("sends what waits in the order it came", func() {
		start(4)
		q.Add("a", "b", "c")
		Expect(q.Flush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c"}))
	})

	It("keeps the order when the items wrap around the end of the ring", func() {
		start(4)
		q.Add("a", "b", "c")
		Expect(q.Flush(context.Background())).To(Succeed())
		q.Add("d", "e", "f", "g")
		Expect(q.Flush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c", "d", "e", "f", "g"}))
	})

	It("refuses the newest items when they do not all fit", func() {
		start(4)
		q.Add("a", "b", "c")
		q.Add("d", "e")
		q.Add("f")
		Expect(q.Waiting()).To(Equal(4))
		Expect(q.Flush(context.Background())).To(Succeed())
		Expect(sent.all()).To(Equal([]string{"a", "b", "c", "f"}))
	})

	It("counts what it refused, in items and in groups", func() {
		start(4)
		q.Add("a", "b", "c")
		q.Add("d", "e")
		q.Add("f", "g")
		items, groups := q.Refused()
		Expect(items).To(Equal(4))
		Expect(groups).To(Equal(2))
	})

	It("refuses a group larger than the whole queue", func() {
		start(4)
		q.Add("a", "b", "c", "d", "e")
		Expect(q.Waiting()).To(Equal(0))
		items, _ := q.Refused()
		Expect(items).To(Equal(5))
	})

	It("sends at most one batch's worth at a time", func() {
		start(1000)
		items := make([]string, 600)
		for i := range items {
			items[i] = string(rune('a' + i%26))
		}
		q.Add(items...)
		Expect(q.Flush(context.Background())).To(Succeed())
		Expect(sent.sizes()).To(Equal([]int{export.BatchSize, 600 - export.BatchSize}))
		Expect(sent.all()).To(Equal(items))
	})
})
