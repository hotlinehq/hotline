package queuefiles

import (
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// What no way in can reach: a file handle that breaks under the folder, as
// a disk that fails does, and a payload whose span lengths do not fit,
// which the crc keeps from ever being read (Peter, 2026-11-28, Q2).
var _ = Describe("disk queue files that break under it", func() {
	sut := brokenFilesSUT{}

	It("ends a list of spans at a length that does not fit", func() {
		Expect(sut.SpansIn([]byte{2, 'a', 'b', 9, 'c'})).To(Equal([]string{"ab"}))
	})

	It("says a write or a sync failed, and writes on in a new file", func() {
		sut.forFolder()
		Expect(sut.Write("a")).To(Succeed())
		sut.BreakTheFileBeingWritten()
		Expect(sut.Sync()).NotTo(Succeed())
		Expect(sut.Write("b")).NotTo(Succeed())
		Expect(sut.Write("c")).To(Succeed())
		read, lost := sut.Read()
		Expect(read).To(Equal([]string{"a", "c"}))
		Expect(lost).To(BeZero())
		sut.BreakThePositionFile()
		sut.Taken()
		Expect(sut.Sync()).NotTo(Succeed())
	})

	It("says a full file switch failed", func() {
		sut.forFolder()
		Expect(sut.Write("a")).To(Succeed())
		sut.BreakTheFileBeingWritten()
		Expect(sut.Write(strings.Repeat("x", queueFileSize))).NotTo(Succeed())
	})
})

// brokenFilesSUT is the queue files of one signal, whose open files the
// test closes under them.
type brokenFilesSUT struct {
	folder *Folder
	last   Position
}

// forFolder is a new folder with a budget of a gigabyte.
func (s *brokenFilesSUT) forFolder() {
	var err error
	var lost int
	s.folder, lost, err = Open(filepath.Join(GinkgoT().TempDir(), "records"), NewBudget(1<<30))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	Expect(lost).WithOffset(1).To(BeZero())
}

// Write writes records.
func (s *brokenFilesSUT) Write(records ...string) error {
	items := make([]Item, len(records))
	for i, r := range records {
		items[i] = Item{Kind: Record, Parts: [][]byte{[]byte(r)}}
	}
	_, err := s.folder.Write(items)
	return err
}

// Read reads up to 10 records, keeping where it ended for Taken.
func (s *brokenFilesSUT) Read() ([]string, int) {
	items, at, lost := s.folder.Read(10)
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
func (s *brokenFilesSUT) Taken() { s.folder.Taken(s.last) }

// Sync syncs what was written.
func (s *brokenFilesSUT) Sync() error { return s.folder.Sync() }

// BreakTheFileBeingWritten closes the file being written under the
// folder, so writes and syncs to it fail.
func (s *brokenFilesSUT) BreakTheFileBeingWritten() { _ = s.folder.w.Close() }

// BreakThePositionFile closes the position file under the folder.
func (s *brokenFilesSUT) BreakThePositionFile() { _ = s.folder.pos.Close() }

// SpansIn reads back the spans of one call from a payload.
func (*brokenFilesSUT) SpansIn(payload []byte) []string {
	var spans []string
	for _, span := range splitSpans(payload) {
		spans = append(spans, string(span))
	}
	return spans
}
