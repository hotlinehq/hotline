package export_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel/metric/noop"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
)

// queueFiles are the names of the queue files in dir, oldest first.
func queueFiles(dir string) []string {
	files, _ := filepath.Glob(filepath.Join(dir, "*.queue"))
	slices.Sort(files)
	return files
}

var _ = Describe("disk queue", func() {
	var dir string
	BeforeEach(func() { dir = filepath.Join(GinkgoT().TempDir(), "records") })
	open := func() export.TestDiskQueue {
		d, lost, err := export.OpenTestDiskQueue(dir, 1<<30)
		Expect(err).NotTo(HaveOccurred())
		Expect(lost).To(BeZero())
		return d
	}

	It("reads back what it wrote, in order, and is empty once all is taken", func() {
		d := open()
		Expect(d.Empty()).To(BeTrue())
		Expect(d.Write("a", "b", "c")).To(Succeed())
		Expect(d.Empty()).To(BeFalse())
		read, lost := d.Read(2)
		Expect(lost).To(BeZero())
		Expect(read).To(Equal([]string{"a", "b"}))
		d.Taken()
		read, _ = d.Read(10)
		Expect(read).To(Equal([]string{"c"}))
		d.Taken()
		Expect(d.Empty()).To(BeTrue())
		Expect(d.Sync()).To(Succeed())
		Expect(d.Close()).To(Succeed())
	})

	It("reads the same items again until they are taken", func() {
		d := open()
		Expect(d.Write("a", "b")).To(Succeed())
		first, _ := d.Read(10)
		again, _ := d.Read(10)
		Expect(again).To(Equal(first))
		Expect(d.Close()).To(Succeed())
	})

	It("stops in the middle of an older file once it has read enough", func() {
		d := open()
		third := strings.Repeat("x", export.QueueFileSize/3)
		Expect(d.Write(third, third, third)).To(Succeed())
		read, _ := d.Read(1)
		Expect(read).To(HaveLen(1))
		d.Taken()
		read, _ = d.Read(10)
		Expect(read).To(HaveLen(2))
		Expect(d.Close()).To(Succeed())
	})

	It("keeps one call's spans together", func() {
		d := open()
		Expect(d.WriteSpans("s1", "s2", "s3")).To(Succeed())
		read, _ := d.Read(1)
		Expect(read).To(Equal([]string{"s1", "s2", "s3"}))
		Expect(d.Close()).To(Succeed())
	})

	It("ends a list of spans at a length that does not fit", func() {
		Expect(export.SplitSpans([]byte{2, 'a', 'b', 9, 'c'})).To(Equal([]string{"ab"}))
	})

	It("starts a new file when one is full, and deletes a file once all of it is taken", func() {
		d := open()
		big := strings.Repeat("x", export.QueueFileSize/3)
		Expect(d.Write(big, big, big)).To(Succeed())
		Expect(queueFiles(dir)).To(HaveLen(2))
		read, _ := d.Read(2)
		Expect(read).To(HaveLen(2))
		d.Taken()
		Expect(queueFiles(dir)).To(HaveLen(1))
		Expect(d.Close()).To(Succeed())
	})

	It("puts an item larger than a whole file in a file of its own", func() {
		d := open()
		Expect(d.Write("a")).To(Succeed())
		Expect(d.Write(strings.Repeat("x", export.QueueFileSize+1))).To(Succeed())
		Expect(queueFiles(dir)).To(HaveLen(2))
		read, _ := d.Read(10)
		Expect(read).To(HaveLen(2))
		Expect(d.Close()).To(Succeed())
	})

	It("keeps what was written through a crash, and goes on from where the sender was", func() {
		d := open()
		Expect(d.Write("a", "b", "c")).To(Succeed())
		_, _ = d.Read(1)
		d.Taken()
		// No sync and no close, as in a crash.
		again := open()
		read, _ := again.Read(10)
		Expect(read).To(Equal([]string{"b", "c"}))
		Expect(again.Close()).To(Succeed())
	})

	It("starts at the oldest file when the position file cannot be read", func() {
		d := open()
		Expect(d.Write("a", "b")).To(Succeed())
		_, _ = d.Read(1)
		d.Taken()
		Expect(d.Close()).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "position"), []byte("broken"), 0o600)).To(Succeed())
		again := open()
		read, _ := again.Read(10)
		Expect(read).To(Equal([]string{"a", "b"}))
		Expect(again.Close()).To(Succeed())
	})

	It("starts at the oldest file when the position names a file that is gone", func() {
		d := open()
		Expect(d.Write("a")).To(Succeed())
		Expect(d.Close()).To(Succeed())
		files := queueFiles(dir)
		Expect(os.Remove(files[0])).To(Succeed())
		again := open()
		read, _ := again.Read(10)
		Expect(read).To(BeEmpty())
		Expect(again.Close()).To(Succeed())
	})

	It("cuts away an item a crash cut short, and counts it", func() {
		d := open()
		Expect(d.Write("a", "b")).To(Succeed())
		Expect(d.Close()).To(Succeed())
		files := queueFiles(dir)
		last := files[len(files)-1]
		info, _ := os.Stat(last)
		Expect(os.Truncate(last, info.Size()-1)).To(Succeed())
		again, lost, err := export.OpenTestDiskQueue(dir, 1<<30)
		Expect(err).NotTo(HaveOccurred())
		Expect(lost).To(Equal(1))
		read, _ := again.Read(10)
		Expect(read).To(Equal([]string{"a"}))
		Expect(again.Close()).To(Succeed())
	})

	It("counts and removes a file with no readable header", func() {
		Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "00000005.queue"), []byte("HT"), 0o600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("kept"), 0o600)).To(Succeed())
		d, lost, err := export.OpenTestDiskQueue(dir, 1<<30)
		Expect(err).NotTo(HaveOccurred())
		Expect(lost).To(Equal(1))
		Expect(filepath.Join(dir, "00000005.queue")).NotTo(BeAnExistingFile())
		Expect(d.Close()).To(Succeed())
	})

	It("removes files the sender had passed before the stop", func() {
		d := open()
		big := strings.Repeat("x", export.QueueFileSize/3)
		Expect(d.Write(big, big, big)).To(Succeed())
		files := queueFiles(dir)
		Expect(files).To(HaveLen(2))
		// The sender moves past the first file, and its deletion is
		// missed, as if the proxy stopped just then.
		backup, _ := os.ReadFile(files[0])
		_, _ = d.Read(2)
		d.Taken()
		Expect(d.Close()).To(Succeed())
		Expect(os.WriteFile(files[0], backup, 0o600)).To(Succeed())
		again := open()
		Expect(files[0]).NotTo(BeAnExistingFile())
		Expect(again.Close()).To(Succeed())
	})

	It("skips to the next file past an item it cannot read, counting the items skipped", func() {
		d := open()
		big := strings.Repeat("x", export.QueueFileSize/3)
		Expect(d.Write(big, "b", big, big)).To(Succeed())
		Expect(d.Close()).To(Succeed())
		files := queueFiles(dir)
		Expect(len(files)).To(BeNumerically(">=", 2))
		data, _ := os.ReadFile(files[0])
		// Break the first item, so the rest of that file cannot be read.
		data[13+9] ^= 0xff
		Expect(os.WriteFile(files[0], data, 0o600)).To(Succeed())
		again := open()
		read, lost := again.Read(10)
		Expect(lost).To(Equal(3))
		Expect(read).To(HaveLen(1))
		Expect(again.Close()).To(Succeed())
	})

	It("skips a broken item in the file being written", func() {
		d := open()
		Expect(d.Write("a")).To(Succeed())
		files := queueFiles(dir)
		data, _ := os.ReadFile(files[0])
		data[len(data)-1] ^= 0xff
		Expect(os.WriteFile(files[0], data, 0o600)).To(Succeed())
		read, _ := d.Read(10)
		Expect(read).To(BeEmpty())
		d.Taken()
		Expect(d.Empty()).To(BeTrue())
		Expect(d.Close()).To(Succeed())
	})

	It("puts items before everything on disk, and sends them first", func() {
		d := open()
		Expect(d.Write("c", "d")).To(Succeed())
		Expect(d.WriteFront()).To(Succeed())
		Expect(d.WriteFront("a", "b")).To(Succeed())
		read, _ := d.Read(10)
		Expect(read).To(Equal([]string{"a", "b", "c", "d"}))
		Expect(d.Close()).To(Succeed())
		again := open()
		read, _ = again.Read(10)
		Expect(read).To(Equal([]string{"a", "b", "c", "d"}))
		Expect(again.Close()).To(Succeed())
	})

	It("takes from the budget, and gives back what a deleted file held", func() {
		d := open()
		start := d.Used()
		big := strings.Repeat("x", export.QueueFileSize/3)
		Expect(d.Write(big, big, big)).To(Succeed())
		Expect(d.Used()).To(BeNumerically(">", start+int64(export.QueueFileSize)))
		_, _ = d.Read(2)
		d.Taken()
		Expect(d.Used()).To(BeNumerically("<", start+int64(export.QueueFileSize)))
		Expect(d.Close()).To(Succeed())
	})

	Context("when the folder or its files fail", func() {
		It("cannot open where a file stands in the folder's place", func() {
			Expect(os.WriteFile(dir, []byte("a file"), 0o600)).To(Succeed())
			_, _, err := export.OpenTestDiskQueue(dir, 1<<30)
			Expect(err).To(HaveOccurred())
		})

		It("cannot open when the name of its new file is taken by a folder that cannot be removed", func() {
			d := open()
			Expect(d.Write("a")).To(Succeed())
			Expect(d.Close()).To(Succeed())
			taken := filepath.Join(dir, "01048577.queue")
			Expect(os.MkdirAll(filepath.Join(taken, "inside"), 0o700)).To(Succeed())
			_, _, err := export.OpenTestDiskQueue(dir, 1<<30)
			Expect(err).To(HaveOccurred())
		})

		It("counts a queue file it cannot open", func() {
			Expect(os.MkdirAll(dir, 0o700)).To(Succeed())
			Expect(os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "00000009.queue"))).To(Succeed())
			d, lost, err := export.OpenTestDiskQueue(dir, 1<<30)
			Expect(err).NotTo(HaveOccurred())
			Expect(lost).To(Equal(1))
			Expect(d.Close()).To(Succeed())
		})

		It("starts at the oldest file when the position file's crc is wrong", func() {
			d := open()
			Expect(d.Write("a")).To(Succeed())
			Expect(d.Close()).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "position"), make([]byte, 24), 0o600)).To(Succeed())
			again := open()
			read, _ := again.Read(10)
			Expect(read).To(Equal([]string{"a"}))
			Expect(again.Close()).To(Succeed())
		})

		It("cannot start a file whose name is taken", func() {
			d := open()
			big := strings.Repeat("x", export.QueueFileSize/3)
			files := queueFiles(dir)
			next := strings.Replace(files[0], "01048576", "01048577", 1)
			Expect(os.Mkdir(next, 0o700)).To(Succeed())
			Expect(d.Write(big, big, big)).NotTo(Succeed())
			Expect(d.Close()).NotTo(Succeed())
		})

		It("says a write or a sync failed", func() {
			d := open()
			Expect(d.Write("a")).To(Succeed())
			d.BreakWriter()
			Expect(d.Write("b")).NotTo(Succeed())
			Expect(d.Sync()).NotTo(Succeed())
			d.BreakPosition()
			d.Taken()
			Expect(d.Sync()).NotTo(Succeed())
		})

		It("says a full file switch failed", func() {
			d := open()
			Expect(d.Write("a")).To(Succeed())
			d.BreakWriter()
			Expect(d.Write(strings.Repeat("x", export.QueueFileSize))).NotTo(Succeed())
		})

		It("skips a file it cannot open, counting what it held", func() {
			d := open()
			third := strings.Repeat("x", export.QueueFileSize/3)
			Expect(d.Write(third, third, third, "c")).To(Succeed())
			files := queueFiles(dir)
			Expect(files).To(HaveLen(2))
			Expect(os.Remove(files[0])).To(Succeed())
			read, lost := d.Read(10)
			Expect(lost).To(Equal(2))
			Expect(read).To(Equal([]string{third, "c"}))
			Expect(d.Close()).To(Succeed())
		})

		It("skips what is left of the file being written when it is shorter than written", func() {
			d := open()
			Expect(d.Write("a", "b")).To(Succeed())
			files := queueFiles(dir)
			Expect(os.Truncate(files[0], 14)).To(Succeed())
			read, lost := d.Read(10)
			Expect(read).To(BeEmpty())
			Expect(lost).To(Equal(1))
			d.Taken()
			Expect(d.Empty()).To(BeTrue())
			Expect(d.Close()).To(Succeed())
		})

		It("says a file before the oldest could not be written", func() {
			if runtime.GOOS != "linux" {
				Skip("needs /dev/full, a file every write to fails")
			}
			d := open()
			Expect(d.Write("b")).To(Succeed())
			files := queueFiles(dir)
			front := strings.Replace(files[0], "01048576", "01048575", 1)
			Expect(os.Symlink("/dev/full", front)).To(Succeed())
			Expect(d.WriteFront("a")).NotTo(Succeed())
			Expect(d.Close()).To(Succeed())
		})
	})
})

// recordingSender keeps what it was given, and answers with what it is
// told, one answer a send.
type recordingSender struct {
	mu      sync.Mutex
	sent    []string
	answers []error
}

func (s *recordingSender) send(_ context.Context, items []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.answers) > 0 {
		err := s.answers[0]
		s.answers = s.answers[1:]
		if err != nil {
			return err
		}
	}
	s.sent = append(s.sent, items...)
	return nil
}

func (s *recordingSender) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

var _ = Describe("a queue with a disk queue behind it", func() {
	var (
		dir    string
		sender *recordingSender
	)
	BeforeEach(func() {
		dir = filepath.Join(GinkgoT().TempDir(), "records")
		sender = &recordingSender{}
	})
	start := func(size int, limit int64) export.TestDiskBackedQueue {
		q, err := export.NewTestDiskBackedQueue(size, dir, limit, sender.send)
		Expect(err).NotTo(HaveOccurred())
		return q
	}

	It("sends memory first, then the disk, and keeps using the disk until it is empty", func() {
		q := start(2, 1<<30)
		q.Add("a", "b")
		q.Add("c")
		Expect(q.OnDisk()).To(BeTrue())
		Eventually(q.Written).Should(BeTrue())
		Expect(q.Flush(context.Background())).To(Succeed())
		Expect(sender.got()).To(Equal([]string{"a", "b", "c"}))
		Expect(q.OnDisk()).To(BeFalse())
		Expect(q.Shutdown(context.Background())).To(Succeed())
	})

	It("keeps a batch on disk while the collector is busy, and drops one it refused for good", func() {
		q := start(1, 1<<30)
		q.Add("a")
		q.Add("b")
		q.Add("c")
		Eventually(q.Written).Should(BeTrue())
		sender.answers = []error{nil, export.ErrTestBusy, errors.New("refused")}
		Expect(q.Flush(context.Background())).To(MatchError(export.ErrTestBusy))
		Expect(q.Flush(context.Background())).To(MatchError("refused"))
		Expect(sender.got()).To(Equal([]string{"a"}))
		Expect(q.Shutdown(context.Background())).To(Succeed())
	})

	It("refuses what the disk budget has no room for", func() {
		q := start(1, 1<<20)
		q.Add("a")
		q.Add(strings.Repeat("x", 2<<20))
		refused, _ := q.Refused()
		Expect(refused).To(Equal(1))
		Expect(q.Shutdown(context.Background())).To(Succeed())
	})

	It("cannot start where a file stands in the folder's place", func() {
		Expect(os.WriteFile(dir, []byte("a file"), 0o600)).To(Succeed())
		_, err := export.NewTestDiskBackedQueue(1, dir, 1<<30, sender.send)
		Expect(err).To(HaveOccurred())
	})

	It("warns when the disk queue cannot be written, and goes on", func() {
		q := start(1, 1<<30)
		q.Add("a")
		q.BreakDiskWriter()
		q.Add("b")
		Eventually(q.Written).Should(BeTrue())
		Expect(q.Shutdown(context.Background())).NotTo(Succeed())
	})

	It("writes what is still in memory to disk at a stop, before what is there", func() {
		q := start(2, 1<<30)
		q.Add("a", "b")
		q.Add("c")
		Eventually(q.Written).Should(BeTrue())
		sender.answers = []error{export.ErrTestBusy, export.ErrTestBusy, export.ErrTestBusy}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		Expect(q.Shutdown(ctx)).To(Succeed())
		d, _, err := export.OpenTestDiskQueue(dir, 1<<30)
		Expect(err).NotTo(HaveOccurred())
		read, _ := d.Read(10)
		Expect(read).To(Equal([]string{"a", "b", "c"}))
		Expect(d.Close()).To(Succeed())
	})
})

var _ = Describe("an exporter's queue folder", func() {
	setup := func(folder string) export.Setup {
		return export.Setup{
			OTLP: []config.Exporter{{
				Endpoint: "http://127.0.0.1:1", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, QueueSize: 10,
				LogInterval: time.Hour, TraceInterval: time.Hour, QueueFolder: folder, QueueDisk: 64 << 20,
			}},
			Logger: slog.New(slog.DiscardHandler),
			Meter:  noop.NewMeterProvider().Meter("test"),
		}
	}

	It("is held by one proxy at a time", func() {
		folder := GinkgoT().TempDir()
		first, err := export.New(setup(folder))
		Expect(err).NotTo(HaveOccurred())
		_, err = export.New(setup(folder))
		Expect(err).To(MatchError(export.ErrFolderTaken))
		Expect(first.Shutdown(context.Background())).To(Succeed())
	})

	It("lets go of the exporters started before one whose folder fails", func() {
		s := setup(filepath.Join(GinkgoT().TempDir(), "missing"))
		s.OTLP = append([]config.Exporter{{
			Endpoint: "http://127.0.0.1:2", URL: &url.URL{Scheme: "http", Host: "127.0.0.1:2"}, QueueSize: 10,
			LogInterval: time.Hour, TraceInterval: time.Hour,
		}}, s.OTLP...)
		_, err := export.New(s)
		Expect(err).To(HaveOccurred())
	})

	It("must exist", func() {
		_, err := export.New(setup(filepath.Join(GinkgoT().TempDir(), "missing")))
		Expect(err).To(HaveOccurred())
	})

	It("cannot hold the spans where a file stands in the spans folder's place", func() {
		folder := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(folder, "spans"), []byte("a file"), 0o600)).To(Succeed())
		_, err := export.New(setup(folder))
		Expect(err).To(HaveOccurred())
	})
})
