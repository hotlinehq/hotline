//go:build unix

package fuzzlog_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/unix"

	"hotline-proxy/internal/fuzzlog"
)

var _ = Describe("capturing a fuzz worker's output", func() {
	sut := captureSUT{}

	It("writes what the worker writes to its standard error to its own file", func() {
		sut.forFuzzWorker()
		sut.CaptureInto(sut.dir)
		sut.WriteToStandardError("panic: boom\n")
		Expect(sut.WorkerLog()).To(Equal("panic: boom\n"))
	})

	It("leaves the output where it was when the folder cannot take the file", func() {
		sut.forFuzzWorker()
		missing := filepath.Join(sut.dir, "missing")
		sut.CaptureInto(missing)
		Expect(missing).NotTo(BeAnExistingFile())
	})

	It("does nothing in a process that is not a fuzz worker", func() {
		sut.forTestProcess()
		sut.CaptureInto(sut.dir)
		Expect(sut.FilesWritten()).To(BeEmpty())
	})
})

// captureSUT is this test process, made to look like a fuzz worker or
// not, with its standard error put back and its arguments restored once
// the test ends.
type captureSUT struct {
	dir string
}

// forFuzzWorker makes this process look like a fuzz worker.
func (s *captureSUT) forFuzzWorker() {
	s.forTestProcess()
	os.Args = append([]string{os.Args[0], "-test.fuzzworker"}, os.Args[1:]...)
}

// forTestProcess is this process as it is, with a folder for the logs.
func (s *captureSUT) forTestProcess() {
	s.dir = GinkgoT().TempDir()
	args := os.Args
	saved, err := unix.Dup(int(os.Stderr.Fd()))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(func() {
		os.Args = args
		Expect(unix.Dup2(saved, int(os.Stderr.Fd()))).To(Succeed())
		Expect(unix.Close(saved)).To(Succeed())
	})
}

// CaptureInto captures the output with folder set as the fuzz log
// folder.
func (*captureSUT) CaptureInto(folder string) {
	GinkgoT().Setenv("HOTLINE_FUZZ_LOGS", folder)
	fuzzlog.Capture()
}

// WriteToStandardError writes text as the worker would.
func (*captureSUT) WriteToStandardError(text string) {
	_, err := os.Stderr.WriteString(text)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
}

// WorkerLog is the log file of this process in the folder.
func (s *captureSUT) WorkerLog() string {
	b, err := fs.ReadFile(os.DirFS(s.dir), "worker-"+strconv.Itoa(os.Getpid())+".log")
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return string(b)
}

// FilesWritten are the files in the folder.
func (s *captureSUT) FilesWritten() []os.DirEntry {
	entries, err := os.ReadDir(s.dir)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return entries
}
