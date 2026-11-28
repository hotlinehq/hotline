package export

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/config"
)

// A rotation that fails cannot be made from outside: a write opens a
// current file that went away again before it rotates, so only the
// rotation itself, called at the moment the test picks, shows what is
// lost (Peter, 2026-11-28, Q2).
var _ = Describe("Rotating the file of call lines", func() {
	sut := rotationSUT{}

	It("loses only the lines of a failed rotation, and writes on after it", func() {
		sut.forWriter()
		sut.Write(`{"call":1}`)
		sut.RemoveTheCurrentFile()
		Expect(sut.Rotate()).NotTo(Succeed())
		sut.Write(`{"call":2}`)
		Expect(sut.CurrentFile()).To(Equal("{\"call\":2}\n"))
	})
})

// rotationSUT is one file writer of call lines in a new folder, which
// the test rotates by hand.
type rotationSUT struct {
	dir    string
	writer *fileWriter
}

// forWriter is a writer that rotates past a megabyte and keeps two
// rotated files, without gzip.
func (s *rotationSUT) forWriter() {
	s.dir = GinkgoT().TempDir()
	var err error
	s.writer, err = openFileWriter(config.FileExporter{Path: s.dir, MaxFileSize: 1 << 20, MaxFiles: 2})
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = s.writer.close() })
}

// Write adds one line, failing the test if it cannot.
func (s *rotationSUT) Write(line string) {
	Expect(s.writer.write([][]byte{[]byte(line + "\n")})).WithOffset(1).To(Succeed())
}

// RemoveTheCurrentFile deletes the file being written, under the writer.
func (s *rotationSUT) RemoveTheCurrentFile() {
	Expect(os.Remove(filepath.Join(s.dir, "calls.jsonl"))).WithOffset(1).To(Succeed())
}

// Rotate starts a new file now.
func (s *rotationSUT) Rotate() error { return s.writer.rotate() }

// CurrentFile is what the file being written holds.
func (s *rotationSUT) CurrentFile() string {
	b, err := os.ReadFile(filepath.Join(s.dir, "calls.jsonl"))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return string(b)
}
