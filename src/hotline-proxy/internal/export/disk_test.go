package export_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/metric/noop"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/queuefiles"
)

// errRefused is a send the collector refused for good.
var errRefused = errors.New("refused")

var _ = Describe("a queue with a disk queue behind it", func() {
	sut := diskQueueSUT{}

	It("sends memory first, then the disk, and keeps using the disk until it is empty", func() {
		sut.forQueueHolding(2)
		Expect(sut.Add("a", "b")).To(BeTrue())
		Expect(sut.Add("c")).To(BeTrue())
		Expect(sut.SentOnceAllAre(3)).To(Equal([]string{"a", "b", "c"}))
		// Once the disk is empty, new items go to memory, and to the disk
		// again only when memory is full again: it is warned of twice.
		Expect(sut.Add("d", "e")).To(BeTrue())
		Expect(sut.Add("f")).To(BeTrue())
		sut.Stop()
		Expect(strings.Count(sut.Logs(), "new items go to the disk queue until it is empty")).To(Equal(2))
	})

	It("keeps a batch on disk while the collector is busy, and drops one it refused for good", func() {
		sut.forQueueHolding(1)
		sut.CollectorAnswers(nil, export.ErrBusy, errRefused)
		Expect(sut.Add("a")).To(BeTrue())
		Expect(sut.Add("b")).To(BeTrue())
		Expect(sut.Add("c")).To(BeTrue())
		sut.WaitForTheCollectorsAnswers()
		Expect(sut.SentOnceAllAre(1)).To(Equal([]string{"a"}))
		sut.Stop()
		Expect(sut.SentItems()).To(Equal([]string{"a"}))
	})

	It("refuses what the disk budget has no room for", func() {
		sut.forQueueHoldingWithDiskBudget(1, 1<<20)
		Expect(sut.Add("a")).To(BeTrue())
		Expect(sut.Add(strings.Repeat("x", 2<<20))).To(BeFalse())
		sut.Stop()
		Expect(sut.Logs()).To(ContainSubstring("export queue and its disk queue full"))
		Expect(sut.Logs()).To(ContainSubstring("dropped=1 calls=1"))
	})

	It("cannot start where a file stands in the folder's place", func() {
		Expect(sut.StartErrorWithAFileInPlaceOfTheFolder()).To(HaveOccurred())
	})

	It("warns when the disk queue cannot be written, counts what it lost, and goes on", func() {
		sut.forQueueHolding(1)
		Expect(sut.Add("a")).To(BeTrue())
		sut.AddWhileFilesAreLimitedToAFewBytes("b")
		Expect(sut.Logs()).To(ContainSubstring("the disk queue could not be written"))
		Expect(sut.Add("c")).To(BeTrue())
		Expect(sut.SentOnceAllAre(2)).To(Equal([]string{"a", "c"}))
		sut.Stop()
		Expect(sut.Logs()).To(ContainSubstring("dropped=1"))
	})

	It("does not send again what was sent before a stop put memory in front", func() {
		sut.forQueueHolding(1)
		Expect(sut.Add("a")).To(BeTrue())
		Expect(sut.Add("b")).To(BeTrue())
		// a and b go; the sender is past b, in the middle of its file.
		Expect(sut.SentOnceAllAre(2)).To(Equal([]string{"a", "b"}))
		// The collector is busy until the stop, so c goes in front of d.
		sut.CollectorIsBusy()
		Expect(sut.Add("c")).To(BeTrue())
		Expect(sut.Add("d")).To(BeTrue())
		sut.StopWithNoTimeLeft()
		sut.CollectorTakesEverything()
		sut.StartAgain()
		Expect(sut.SentOnceAllAre(4)).To(Equal([]string{"a", "b", "c", "d"}))
	})

	It("writes what is still in memory to disk at a stop, before what is there", func() {
		sut.forQueueHolding(2)
		sut.CollectorIsBusy()
		Expect(sut.Add("a", "b")).To(BeTrue())
		Expect(sut.Add("c")).To(BeTrue())
		sut.StopWithNoTimeLeft()
		Expect(sut.ItemsLeftOnDisk()).To(Equal([]string{"a", "b", "c"}))
	})
})

var _ = Describe("an exporter's queue folder", func() {
	sut := queueFolderSUT{}

	It("is held by one proxy at a time", func() {
		sut.forExporterUsingAQueueFolder()
		Expect(sut.StartAnotherOnTheSameFolder()).To(MatchError(export.ErrFolderTaken))
	})

	// The oldest item waits in memory, then on disk, while the collector
	// is busy.
	It("knows when the oldest waiting item was queued, in memory and then on disk", func() {
		sut.forExporterUsingAQueueFolder()
		Expect(sut.OldestRecordWaitedSeconds()).To(BeZero())
		sut.ExportCall()
		Expect(sut.OldestRecordWaitedSeconds()).To(BeNumerically(">", 0))
		sut.ExportCall()
		Expect(sut.OldestRecordWaitedSeconds()).To(BeNumerically(">", 0))
	})

	It("lets go of the exporters started before one whose folder fails", func() {
		Expect(sut.StartErrorAfterAnExporterWithNoFolder(filepath.Join(GinkgoT().TempDir(), "missing"))).To(HaveOccurred())
	})

	It("must exist", func() {
		Expect(sut.StartError(filepath.Join(GinkgoT().TempDir(), "missing"))).To(HaveOccurred())
	})

	It("cannot hold the spans where a file stands in the spans folder's place", func() {
		Expect(sut.StartErrorWithAFileInPlaceOfTheSpansFolder()).To(HaveOccurred())
	})
})

// diskQueueSUT is one export queue of text records with a disk queue
// behind it, sending to a fake collector that takes everything unless the
// test tells it otherwise, and the log the queue warns in.
type diskQueueSUT struct {
	dir     string
	size    int
	limit   int64
	queue   *export.Queue
	logs    *lockedLog
	stopped bool

	mu sync.Mutex
	// sent are the records the collector took, answers what it answers the
	// next sends, and busy that it is busy for every send after them.
	sent    []string
	answers []error
	busy    bool
	asked   int
}

// forQueueHolding is a queue that holds size records in memory and the
// rest on disk, up to a gigabyte.
func (s *diskQueueSUT) forQueueHolding(size int) { s.forQueueHoldingWithDiskBudget(size, 1<<30) }

// forQueueHoldingWithDiskBudget is a queue that holds size records in
// memory and the rest on disk, up to limit bytes.
func (s *diskQueueSUT) forQueueHoldingWithDiskBudget(size int, limit int64) {
	s.dir, s.size, s.limit, s.logs = filepath.Join(GinkgoT().TempDir(), "records"), size, limit, &lockedLog{}
	s.mu.Lock()
	s.sent, s.answers, s.busy, s.asked = nil, nil, false, 0
	s.mu.Unlock()
	queue, err := s.start()
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.queue = queue
	DeferCleanup(s.Close)
}

// StartAgain starts a new queue on the same folder, as the next run of
// the proxy does.
func (s *diskQueueSUT) StartAgain() {
	queue, err := s.start()
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.queue, s.stopped = queue, false
}

// StartErrorWithAFileInPlaceOfTheFolder is why a queue cannot start where
// a file stands in its folder's place.
func (s *diskQueueSUT) StartErrorWithAFileInPlaceOfTheFolder() error {
	s.dir, s.size, s.limit, s.logs = filepath.Join(GinkgoT().TempDir(), "records"), 1, 1<<30, &lockedLog{}
	Expect(os.WriteFile(s.dir, []byte("a file"), 0o600)).WithOffset(1).To(Succeed())
	_, err := s.start()
	return err
}

// start is a queue that sends every hour, or when what it wrote to disk
// wakes it, and when it is flushed.
func (s *diskQueueSUT) start() (*export.Queue, error) {
	counter, err := noop.NewMeterProvider().Meter("test").Int64Counter("test")
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	return export.NewQueue(export.QueueSetup{
		ItemName: "records", Size: s.size, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(s.logs, nil)),
		Counts: export.QueueCounts{Sent: counter, Dropped: counter}, ExporterUp: &atomic.Int64{}, Send: s.collect,
		Disk: export.QueueDisk{Folder: s.dir, Budget: queuefiles.NewBudget(s.limit)},
	})
}

// Close stops the queue, if the test did not already.
func (s *diskQueueSUT) Close() {
	if !s.stopped {
		s.StopWithNoTimeLeft()
	}
}

// Add queues records: all of them, or none. It says whether they were
// queued.
func (s *diskQueueSUT) Add(records ...string) bool { return s.queue.Add(texts(records)...) }

// AddWhileFilesAreLimitedToAFewBytes queues a record while this process
// may write no file past 20 bytes, so the disk queue cannot write it,
// and waits until the queue has warned of that. Go ignores the signal
// that comes with going past the limit.
func (s *diskQueueSUT) AddWhileFilesAreLimitedToAFewBytes(record string) {
	var limit syscall.Rlimit
	Expect(syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit)).WithOffset(1).To(Succeed())
	small := limit
	small.Cur = 20
	Expect(syscall.Setrlimit(syscall.RLIMIT_FSIZE, &small)).WithOffset(1).To(Succeed())
	defer func() { Expect(syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit)).WithOffset(1).To(Succeed()) }()
	Expect(s.Add(record)).WithOffset(1).To(BeTrue())
	Eventually(s.Logs).WithOffset(1).Should(ContainSubstring("the disk queue could not be written"))
}

// CollectorAnswers makes the collector answer the next sends with these,
// in order, and take everything after them; nil takes the send.
func (s *diskQueueSUT) CollectorAnswers(answers ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers, s.asked = answers, 0
}

// CollectorIsBusy makes the collector too busy for every send.
func (s *diskQueueSUT) CollectorIsBusy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy = true
}

// CollectorTakesEverything makes the collector take every send.
func (s *diskQueueSUT) CollectorTakesEverything() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.answers = false, nil
}

// WaitForTheCollectorsAnswers waits until the collector has given every
// answer it was told to; the queue tries a busy collector again after
// about a second.
func (s *diskQueueSUT) WaitForTheCollectorsAnswers() {
	Eventually(func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.answers) == 0
	}).WithOffset(1).WithTimeout(10 * time.Second).Should(BeTrue())
}

// SentOnceAllAre flushes the queue until the collector has taken n
// records, and gives them in the order they came.
func (s *diskQueueSUT) SentOnceAllAre(n int) []string {
	Eventually(func() []string {
		_ = s.queue.ForceFlush(context.Background())
		return s.SentItems()
	}).WithOffset(1).WithTimeout(10 * time.Second).Should(HaveLen(n))
	return s.SentItems()
}

// SentItems are the records the collector took, in order.
func (s *diskQueueSUT) SentItems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Stop sends what waits, warns about what was refused, and stops the
// queue.
func (s *diskQueueSUT) Stop() {
	s.stopped = true
	Expect(s.queue.Shutdown(context.Background())).WithOffset(1).To(Succeed())
}

// StopWithNoTimeLeft stops the queue with no time left to send, so what
// is in memory goes to disk.
func (s *diskQueueSUT) StopWithNoTimeLeft() {
	s.stopped = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Expect(s.queue.Shutdown(ctx)).WithOffset(1).To(Succeed())
}

// ItemsLeftOnDisk are the records the queue's folder holds, read as the
// next run would.
func (s *diskQueueSUT) ItemsLeftOnDisk() []string {
	folder, _, err := queuefiles.Open(s.dir, queuefiles.NewBudget(s.limit))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(folder.Close)
	items, _, _ := folder.Read(10)
	var read []string
	for _, item := range items {
		for _, part := range item.Parts {
			read = append(read, string(part))
		}
	}
	return read
}

// Logs is what the queue logged so far.
func (s *diskQueueSUT) Logs() string { return s.logs.String() }

// collect is the fake collector.
func (s *diskQueueSUT) collect(_ context.Context, items [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.answers) > 0 {
		err := s.answers[0]
		s.answers = s.answers[1:]
		if err != nil {
			return err
		}
	} else if s.busy {
		return export.ErrBusy
	}
	for _, item := range items {
		s.sent = append(s.sent, string(item))
	}
	return nil
}

// queueFolderSUT is the exporters of one OTLP exporter with a queue
// folder, sending to an address where nothing answers, so everything
// waits.
type queueFolderSUT struct {
	exporters exportersSUT
	folder    string
}

// forExporterUsingAQueueFolder is an exporter holding 1 record in memory,
// and the rest in a new queue folder.
func (s *queueFolderSUT) forExporterUsingAQueueFolder() {
	s.folder = GinkgoT().TempDir()
	s.exporters.forOTLPExporter(s.exporterWithFolder(s.folder))
}

func (*queueFolderSUT) exporterWithFolder(folder string) config.Exporter {
	return config.Exporter{
		Endpoint: "http://127.0.0.1:1", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, QueueSize: 1,
		LogInterval: time.Hour, TraceInterval: time.Hour, QueueFolder: folder, QueueDisk: 64 << 20,
	}
}

// StartAnotherOnTheSameFolder is why a second exporter on the same folder
// could not start.
func (s *queueFolderSUT) StartAnotherOnTheSameFolder() error {
	return s.startError(s.exporterWithFolder(s.folder))
}

// StartError is why an exporter with this queue folder could not start.
func (s *queueFolderSUT) StartError(folder string) error {
	return s.startError(s.exporterWithFolder(folder))
}

// StartErrorAfterAnExporterWithNoFolder is why exporters could not start
// when the one with this queue folder comes after one with no folder.
func (s *queueFolderSUT) StartErrorAfterAnExporterWithNoFolder(folder string) error {
	first := config.Exporter{
		Endpoint: "http://127.0.0.1:2", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:2"}, QueueSize: 10,
		LogInterval: time.Hour, TraceInterval: time.Hour,
	}
	return s.startError(first, s.exporterWithFolder(folder))
}

// StartErrorWithAFileInPlaceOfTheSpansFolder is why an exporter could not
// start when a file stands where its spans folder goes.
func (s *queueFolderSUT) StartErrorWithAFileInPlaceOfTheSpansFolder() error {
	folder := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(folder, "spans"), []byte("a file"), 0o600)).WithOffset(1).To(Succeed())
	return s.StartError(folder)
}

func (*queueFolderSUT) startError(exporters ...config.Exporter) error {
	started, err := export.New(export.Setup{
		OTLP: exporters, Logger: slog.New(slog.DiscardHandler), Meter: noop.NewMeterProvider().Meter("test"),
	})
	if err == nil {
		DeferCleanup(started.Shutdown, context.Background())
	}
	return err
}

// ExportCall exports one call, whose record waits.
func (s *queueFolderSUT) ExportCall() {
	at := time.Now()
	s.exporters.Call(export.Call{At: at, Observed: at})
}

// OldestRecordWaitedSeconds is how long the oldest record waiting for the
// exporter has waited, as its metric says; 0 when none waits.
func (s *queueFolderSUT) OldestRecordWaitedSeconds() float64 {
	return s.exporters.Gauge("hotline.exporter.queue.oldest.age", "record")
}

func texts(items []string) [][]byte {
	b := make([][]byte, len(items))
	for i, item := range items {
		b[i] = []byte(item)
	}
	return b
}
