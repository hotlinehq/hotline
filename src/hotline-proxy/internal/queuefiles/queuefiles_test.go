package queuefiles_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/queuefiles"
)

// A queue file holds 16 MiB at most, so three items of a third each fill
// one file and start the next.
var _ = Describe("disk queue", func() {
	sut := folderSUT{}

	It("reads back what it wrote, in order, and is empty once all is taken", func() {
		sut.forEmptyFolder()
		Expect(sut.Empty()).To(BeTrue())
		Expect(sut.Write("a", "b", "c")).To(Succeed())
		Expect(sut.Empty()).To(BeFalse())
		read, lost := sut.Read(2)
		Expect(lost).To(BeZero())
		Expect(read).To(Equal([]string{"a", "b"}))
		sut.Taken()
		read, _ = sut.Read(10)
		Expect(read).To(Equal([]string{"c"}))
		sut.Taken()
		Expect(sut.Empty()).To(BeTrue())
		Expect(sut.Sync()).To(Succeed())
		Expect(sut.Close()).To(Succeed())
	})

	It("writes a record item with no parts as an empty record", func() {
		sut.forEmptyFolder()
		Expect(sut.WriteRecordWithNoParts()).To(Succeed())
		read, lost := sut.Read(10)
		Expect(lost).To(BeZero())
		Expect(read).To(Equal([]string{""}))
		Expect(sut.Close()).To(Succeed())
	})
	It("reads the same items again until they are taken", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a", "b")).To(Succeed())
		first, _ := sut.Read(10)
		again, _ := sut.Read(10)
		Expect(again).To(Equal(first))
		Expect(sut.Close()).To(Succeed())
	})

	It("stops in the middle of an older file once it has read enough", func() {
		sut.forEmptyFolder()
		third := sut.ThirdOfAFile()
		Expect(sut.Write(third, third, third)).To(Succeed())
		read, _ := sut.Read(1)
		Expect(read).To(HaveLen(1))
		sut.Taken()
		read, _ = sut.Read(10)
		Expect(read).To(HaveLen(2))
		Expect(sut.Close()).To(Succeed())
	})

	It("keeps one call's spans together", func() {
		sut.forEmptyFolder()
		Expect(sut.WriteSpans("s1", "s2", "s3")).To(Succeed())
		read, _ := sut.Read(1)
		Expect(read).To(Equal([]string{"s1", "s2", "s3"}))
		Expect(sut.Close()).To(Succeed())
	})

	It("starts a new file when one is full, and deletes a file once all of it is taken", func() {
		sut.forEmptyFolder()
		third := sut.ThirdOfAFile()
		Expect(sut.Write(third, third, third)).To(Succeed())
		Expect(sut.QueueFiles()).To(HaveLen(2))
		read, _ := sut.Read(2)
		Expect(read).To(HaveLen(2))
		sut.Taken()
		Expect(sut.QueueFiles()).To(HaveLen(1))
		Expect(sut.Close()).To(Succeed())
	})

	It("puts an item larger than a whole file in a file of its own", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a")).To(Succeed())
		Expect(sut.Write(strings.Repeat("x", 16<<20+1))).To(Succeed())
		Expect(sut.QueueFiles()).To(HaveLen(2))
		read, _ := sut.Read(10)
		Expect(read).To(HaveLen(2))
		Expect(sut.Close()).To(Succeed())
	})

	It("keeps what was written through a crash, and goes on from where the sender was", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a", "b", "c")).To(Succeed())
		_, _ = sut.Read(1)
		sut.Taken()
		// No sync and no close, as in a crash.
		Expect(sut.OpenAgain()).To(BeZero())
		read, _ := sut.Read(10)
		Expect(read).To(Equal([]string{"b", "c"}))
		Expect(sut.Close()).To(Succeed())
	})

	It("starts at the oldest file when the position file cannot be read", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a", "b")).To(Succeed())
		_, _ = sut.Read(1)
		sut.Taken()
		Expect(sut.Close()).To(Succeed())
		sut.ReplacePositionFile([]byte("broken"))
		Expect(sut.OpenAgain()).To(BeZero())
		read, _ := sut.Read(10)
		Expect(read).To(Equal([]string{"a", "b"}))
		Expect(sut.Close()).To(Succeed())
	})

	It("starts at the oldest file when the position names a file that is gone", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a")).To(Succeed())
		Expect(sut.Close()).To(Succeed())
		sut.RemoveQueueFile(0)
		Expect(sut.OpenAgain()).To(BeZero())
		read, _ := sut.Read(10)
		Expect(read).To(BeEmpty())
		Expect(sut.Close()).To(Succeed())
	})

	It("cuts away an item a crash cut short, and counts it", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a", "b")).To(Succeed())
		Expect(sut.Close()).To(Succeed())
		sut.CutTheNewestFileShortBy(1)
		Expect(sut.OpenAgain()).To(Equal(1))
		read, _ := sut.Read(10)
		Expect(read).To(Equal([]string{"a"}))
		Expect(sut.Close()).To(Succeed())
	})

	It("counts and removes a file with no readable header", func() {
		sut.forFolderHolding(map[string]string{"00000005.queue": "HT", "notes.txt": "kept"})
		Expect(sut.Open()).To(Equal(1))
		Expect(sut.InFolder("00000005.queue")).NotTo(BeAnExistingFile())
		Expect(sut.Close()).To(Succeed())
	})

	It("removes files the sender had passed before the stop", func() {
		sut.forEmptyFolder()
		third := sut.ThirdOfAFile()
		Expect(sut.Write(third, third, third)).To(Succeed())
		files := sut.QueueFiles()
		Expect(files).To(HaveLen(2))
		// The sender moves past the first file, and its deletion is
		// missed, as if the proxy stopped just then.
		restore := sut.KeepACopyOf(files[0])
		_, _ = sut.Read(2)
		sut.Taken()
		Expect(sut.Close()).To(Succeed())
		restore()
		Expect(sut.OpenAgain()).To(BeZero())
		Expect(files[0]).NotTo(BeAnExistingFile())
		Expect(sut.Close()).To(Succeed())
	})

	It("skips to the next file past an item it cannot read, counting the items skipped", func() {
		sut.forEmptyFolder()
		third := sut.ThirdOfAFile()
		Expect(sut.Write(third, "b", third, third)).To(Succeed())
		Expect(sut.Close()).To(Succeed())
		Expect(len(sut.QueueFiles())).To(BeNumerically(">=", 2))
		// Break the first item, after the file's 13 byte header and 9 bytes
		// into the item, so the rest of that file cannot be read.
		sut.FlipByte(0, 13+9)
		Expect(sut.OpenAgain()).To(BeZero())
		read, lost := sut.Read(10)
		Expect(lost).To(Equal(3))
		Expect(read).To(HaveLen(1))
		Expect(sut.Close()).To(Succeed())
	})

	It("skips a broken item in the file being written", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("a")).To(Succeed())
		sut.FlipByte(0, -1)
		read, _ := sut.Read(10)
		Expect(read).To(BeEmpty())
		sut.Taken()
		Expect(sut.Empty()).To(BeTrue())
		Expect(sut.Close()).To(Succeed())
	})

	It("puts items before everything on disk, and sends them first", func() {
		sut.forEmptyFolder()
		Expect(sut.Write("c", "d")).To(Succeed())
		Expect(sut.WriteFront()).To(Succeed())
		Expect(sut.WriteFront("a", "b")).To(Succeed())
		read, _ := sut.Read(10)
		Expect(read).To(Equal([]string{"a", "b", "c", "d"}))
		Expect(sut.Close()).To(Succeed())
		Expect(sut.OpenAgain()).To(BeZero())
		read, _ = sut.Read(10)
		Expect(read).To(Equal([]string{"a", "b", "c", "d"}))
		Expect(sut.Close()).To(Succeed())
	})

	It("takes from the budget, and gives back what a deleted file held", func() {
		third := sut.ThirdOfAFile()
		// Room for the three items, with their 17 byte frames, two file
		// headers of 13 bytes, and a little more, but not a fourth.
		sut.forEmptyFolderWithBudget(3*int64(len(third)+17) + 2*13 + 1000)
		Expect(sut.Write(third, third, third)).To(Succeed())
		Expect(sut.BudgetHasRoomFor(third)).To(BeFalse())
		_, _ = sut.Read(2)
		sut.Taken()
		Expect(sut.BudgetHasRoomFor(third)).To(BeTrue())
		Expect(sut.Close()).To(Succeed())
	})

	Context("when the folder or its files fail", func() {
		sut := folderSUT{}

		It("cannot open where a file stands in the folder's place", func() {
			sut.forFileInPlaceOfTheFolder()
			Expect(sut.OpenError()).To(HaveOccurred())
		})

		It("cannot open when the name of its new file is taken by a folder that cannot be removed", func() {
			sut.forEmptyFolder()
			Expect(sut.Write("a")).To(Succeed())
			Expect(sut.Close()).To(Succeed())
			sut.PutAFolderWithAFileInsideAt("01048577.queue")
			Expect(sut.OpenError()).To(HaveOccurred())
		})

		It("counts a queue file it cannot open", func() {
			sut.forFolderHoldingALinkToNowhereAt("00000009.queue")
			Expect(sut.Open()).To(Equal(1))
			Expect(sut.Close()).To(Succeed())
		})

		It("starts at the oldest file when the position file's crc is wrong", func() {
			sut.forEmptyFolder()
			Expect(sut.Write("a")).To(Succeed())
			Expect(sut.Close()).To(Succeed())
			sut.ReplacePositionFile(make([]byte, 24))
			Expect(sut.OpenAgain()).To(BeZero())
			read, _ := sut.Read(10)
			Expect(read).To(Equal([]string{"a"}))
			Expect(sut.Close()).To(Succeed())
		})

		It("cannot start a file whose name is taken", func() {
			sut.forEmptyFolder()
			third := sut.ThirdOfAFile()
			sut.PutAFolderAt("01048577.queue")
			Expect(sut.Write(third, third, third)).NotTo(Succeed())
			Expect(sut.Close()).NotTo(Succeed())
		})

		It("skips a file it cannot open, counting what it held", func() {
			sut.forEmptyFolder()
			third := sut.ThirdOfAFile()
			Expect(sut.Write(third, third, third, "c")).To(Succeed())
			Expect(sut.QueueFiles()).To(HaveLen(2))
			sut.RemoveQueueFile(0)
			read, lost := sut.Read(10)
			Expect(lost).To(Equal(2))
			Expect(read).To(Equal([]string{third, "c"}))
			Expect(sut.Close()).To(Succeed())
		})

		It("skips what is left of the file being written when it is shorter than written", func() {
			sut.forEmptyFolder()
			Expect(sut.Write("a", "b")).To(Succeed())
			sut.CutQueueFileTo(0, 14)
			read, lost := sut.Read(10)
			Expect(read).To(BeEmpty())
			Expect(lost).To(Equal(1))
			sut.Taken()
			Expect(sut.Empty()).To(BeTrue())
			Expect(sut.Close()).To(Succeed())
		})

		It("says a file before the oldest could not be written", func() {
			sut.forEmptyFolder()
			Expect(sut.Write("b")).To(Succeed())
			Expect(sut.WriteFrontWithFilesLimitedToAMegabyte(strings.Repeat("a", 2<<20))).NotTo(Succeed())
			Expect(sut.Close()).To(Succeed())
		})
	})
})

// folderSUT is the queue files of one signal in a new folder, with a
// budget of a gigabyte unless the test says otherwise, and the files and
// folder it may break from outside.
type folderSUT struct {
	dir    string
	limit  int64
	budget *queuefiles.Budget
	folder *queuefiles.Folder
	// last is where the last Read ended, for Taken.
	last queuefiles.Position
}

// forEmptyFolder is a folder opened for the first time.
func (s *folderSUT) forEmptyFolder() { s.forEmptyFolderWithBudget(1 << 30) }

// forEmptyFolderWithBudget is a folder opened for the first time, with a
// budget of limit bytes.
func (s *folderSUT) forEmptyFolderWithBudget(limit int64) {
	s.prepare(limit)
	Expect(s.Open()).WithOffset(1).To(BeZero())
}

// forFolderHolding is a folder that holds these files, not opened yet.
func (s *folderSUT) forFolderHolding(files map[string]string) {
	s.prepare(1 << 30)
	Expect(os.MkdirAll(s.dir, 0o700)).WithOffset(1).To(Succeed())
	for name, content := range files {
		Expect(os.WriteFile(s.InFolder(name), []byte(content), 0o600)).WithOffset(1).To(Succeed())
	}
}

// forFolderHoldingALinkToNowhereAt is a folder whose file name is a link
// to a file that does not exist, not opened yet.
func (s *folderSUT) forFolderHoldingALinkToNowhereAt(name string) {
	s.prepare(1 << 30)
	Expect(os.MkdirAll(s.dir, 0o700)).WithOffset(1).To(Succeed())
	Expect(os.Symlink(s.InFolder("nowhere"), s.InFolder(name))).WithOffset(1).To(Succeed())
}

// forFileInPlaceOfTheFolder is a file where the folder should be.
func (s *folderSUT) forFileInPlaceOfTheFolder() {
	s.prepare(1 << 30)
	Expect(os.WriteFile(s.dir, []byte("a file"), 0o600)).WithOffset(1).To(Succeed())
}

func (s *folderSUT) prepare(limit int64) {
	s.dir, s.limit, s.folder, s.last = filepath.Join(GinkgoT().TempDir(), "records"), limit, nil, queuefiles.Position{}
}

// Open opens the folder with a new budget, and says how many items it
// found it could not read.
func (s *folderSUT) Open() int {
	s.budget = queuefiles.NewBudget(s.limit)
	folder, lost, err := queuefiles.Open(s.dir, s.budget)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.folder = folder
	return lost
}

// OpenAgain opens the folder again, as a proxy started after a stop or a
// crash does, and says how many items it could not read.
func (s *folderSUT) OpenAgain() int {
	s.last = queuefiles.Position{}
	return s.Open()
}

// OpenError is why the folder cannot be opened.
func (s *folderSUT) OpenError() error {
	folder, _, err := queuefiles.Open(s.dir, queuefiles.NewBudget(s.limit))
	if err == nil {
		Expect(folder.Close()).WithOffset(1).To(Succeed())
	}
	return err
}

// Write writes records, each taken from the budget first.
func (s *folderSUT) Write(records ...string) error {
	items := make([]queuefiles.Item, len(records))
	for i, r := range records {
		items[i] = queuefiles.Item{Kind: queuefiles.Record, Parts: [][]byte{[]byte(r)}}
		s.budget.Take(items[i].Size())
	}
	_, err := s.folder.Write(items)
	return err
}

// WriteRecordWithNoParts writes a record item that holds no part.
func (s *folderSUT) WriteRecordWithNoParts() error {
	_, err := s.folder.Write([]queuefiles.Item{{Kind: queuefiles.Record}})
	return err
}

// WriteSpans writes one call's spans as one item.
func (s *folderSUT) WriteSpans(spans ...string) error {
	_, err := s.folder.Write([]queuefiles.Item{{Kind: queuefiles.Spans, Parts: texts(spans)}})
	return err
}

// WriteFront puts records before everything on disk.
func (s *folderSUT) WriteFront(records ...string) error {
	items := make([]queuefiles.Item, len(records))
	for i, r := range records {
		items[i] = queuefiles.Item{Kind: queuefiles.Record, Parts: [][]byte{[]byte(r)}}
	}
	return s.folder.WriteFront(items)
}

// WriteFrontWithFilesLimitedToAMegabyte puts records before everything on
// disk while this process may write no file larger than a megabyte. Go
// ignores the signal that comes with going past it.
func (s *folderSUT) WriteFrontWithFilesLimitedToAMegabyte(records ...string) error {
	var limit syscall.Rlimit
	Expect(syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit)).WithOffset(1).To(Succeed())
	small := limit
	small.Cur = 1 << 20
	Expect(syscall.Setrlimit(syscall.RLIMIT_FSIZE, &small)).WithOffset(1).To(Succeed())
	err := s.WriteFront(records...)
	Expect(syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit)).WithOffset(1).To(Succeed())
	return err
}

// Read reads up to limit records or spans, keeping where it ended for
// Taken, and says how many it could not read.
func (s *folderSUT) Read(limit int) ([]string, int) {
	items, at, lost := s.folder.Read(limit)
	s.last = at
	var read []string
	for _, item := range items {
		for _, part := range item.Parts {
			read = append(read, string(part))
		}
	}
	return read, lost
}

// Taken moves past what the last Read read.
func (s *folderSUT) Taken() { s.folder.Taken(s.last) }

// Empty says everything written was read and taken.
func (s *folderSUT) Empty() bool { return s.folder.Empty() }

// Sync syncs what was written.
func (s *folderSUT) Sync() error { return s.folder.Sync() }

// Close syncs and closes the files.
func (s *folderSUT) Close() error { return s.folder.Close() }

// BudgetHasRoomFor says the budget could still take a record of this
// text, and gives the bytes back.
func (s *folderSUT) BudgetHasRoomFor(record string) bool {
	size := queuefiles.Item{Kind: queuefiles.Record, Parts: [][]byte{[]byte(record)}}.Size()
	if !s.budget.Take(size) {
		return false
	}
	s.budget.GiveBack(size)
	return true
}

// ThirdOfAFile is a record a third the size of a queue file.
func (*folderSUT) ThirdOfAFile() string { return strings.Repeat("x", (16<<20)/3) }

// QueueFiles are the paths of the queue files, oldest first.
func (s *folderSUT) QueueFiles() []string {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.queue"))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	slices.Sort(files)
	return files
}

// InFolder is the path of name in the folder.
func (s *folderSUT) InFolder(name string) string { return filepath.Join(s.dir, name) }

// ReplacePositionFile writes content in place of the position file.
func (s *folderSUT) ReplacePositionFile(content []byte) {
	Expect(os.WriteFile(s.InFolder("position"), content, 0o600)).WithOffset(1).To(Succeed())
}

// RemoveQueueFile deletes the queue file at place i, oldest first.
func (s *folderSUT) RemoveQueueFile(i int) {
	Expect(os.Remove(s.QueueFiles()[i])).WithOffset(1).To(Succeed())
}

// CutTheNewestFileShortBy cuts n bytes off the end of the newest queue
// file.
func (s *folderSUT) CutTheNewestFileShortBy(n int64) {
	files := s.QueueFiles()
	last := files[len(files)-1]
	info, err := os.Stat(last)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	Expect(os.Truncate(last, info.Size()-n)).WithOffset(1).To(Succeed())
}

// CutQueueFileTo cuts the queue file at place i to size bytes.
func (s *folderSUT) CutQueueFileTo(i int, size int64) {
	Expect(os.Truncate(s.QueueFiles()[i], size)).WithOffset(1).To(Succeed())
}

// FlipByte flips every bit of one byte of the queue file at place i: at
// offset from its start, or counted from its end when offset is negative.
func (s *folderSUT) FlipByte(i, offset int) {
	name := filepath.Base(s.QueueFiles()[i])
	data, err := s.root().ReadFile(name)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	if offset < 0 {
		offset += len(data)
	}
	data[offset] ^= 0xff
	Expect(s.root().WriteFile(name, data, 0o600)).WithOffset(1).To(Succeed())
}

// KeepACopyOf copies a file of the folder, and gives a function that puts
// the copy back.
func (s *folderSUT) KeepACopyOf(file string) func() {
	name := filepath.Base(file)
	backup, err := s.root().ReadFile(name)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return func() { Expect(s.root().WriteFile(name, backup, 0o600)).WithOffset(1).To(Succeed()) }
}

// root opens the folder, so a name read or written in it cannot leave it.
func (s *folderSUT) root() *os.Root {
	root, err := os.OpenRoot(s.dir)
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	DeferCleanup(root.Close)
	return root
}

// PutAFolderAt makes an empty folder at name in the folder.
func (s *folderSUT) PutAFolderAt(name string) {
	Expect(os.Mkdir(s.InFolder(name), 0o700)).WithOffset(1).To(Succeed())
}

// PutAFolderWithAFileInsideAt makes a folder at name that is not empty,
// so it cannot be removed.
func (s *folderSUT) PutAFolderWithAFileInsideAt(name string) {
	Expect(os.MkdirAll(filepath.Join(s.InFolder(name), "inside"), 0o700)).WithOffset(1).To(Succeed())
}

func texts(items []string) [][]byte {
	b := make([][]byte, len(items))
	for i, item := range items {
		b[i] = []byte(item)
	}
	return b
}
