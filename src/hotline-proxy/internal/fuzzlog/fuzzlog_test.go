package fuzzlog_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/fuzzlog"
)

var _ = Describe("where a fuzz worker writes its output", func() {
	sut := logPathSUT{}

	It("is a file of its own in the folder, named by its process", func() {
		sut.forFuzzWorker()
		path, ok := sut.PathIn("/tmp/fuzz-logs")
		Expect(ok).To(BeTrue())
		Expect(path).To(Equal("/tmp/fuzz-logs/worker-4699.log"))
	})

	It("is nowhere when no folder is set", func() {
		sut.forFuzzWorker()
		_, ok := sut.PathIn("")
		Expect(ok).To(BeFalse())
	})

	It("is nowhere for a process that is not a fuzz worker", func() {
		sut.forFuzzingParent()
		_, ok := sut.PathIn("/tmp/fuzz-logs")
		Expect(ok).To(BeFalse())
	})
})

// logPathSUT is a test process, process 4699, by its arguments.
type logPathSUT struct{ args []string }

// forFuzzWorker is a fuzz worker Go started.
func (s *logPathSUT) forFuzzWorker() {
	s.args = []string{"/tmp/go-build/proxy.test", "-test.fuzzworker", "-test.run=^$"}
}

// forFuzzingParent is the process that starts the fuzz workers.
func (s *logPathSUT) forFuzzingParent() {
	s.args = []string{"/tmp/go-build/proxy.test", "-test.fuzz=^FuzzRequest$"}
}

// PathIn is where the process writes its output, with folder set as the
// folder for fuzz logs, and whether it writes it anywhere.
func (s *logPathSUT) PathIn(folder string) (string, bool) { return fuzzlog.Path(s.args, folder, 4699) }
