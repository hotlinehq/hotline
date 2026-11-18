package fuzzlog_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/fuzzlog"
)

var _ = Describe("where a fuzz worker writes its output", func() {
	worker := []string{"/tmp/go-build/proxy.test", "-test.fuzzworker", "-test.run=^$"}

	It("is a file of its own in the folder, named by its process", func() {
		path, ok := fuzzlog.Path(worker, "/tmp/fuzz-logs", 4699)
		Expect(ok).To(BeTrue())
		Expect(path).To(Equal("/tmp/fuzz-logs/worker-4699.log"))
	})

	It("is nowhere when no folder is set", func() {
		_, ok := fuzzlog.Path(worker, "", 4699)
		Expect(ok).To(BeFalse())
	})

	It("is nowhere for a process that is not a fuzz worker", func() {
		_, ok := fuzzlog.Path([]string{"/tmp/go-build/proxy.test", "-test.fuzz=^FuzzRequest$"}, "/tmp/fuzz-logs", 4699)
		Expect(ok).To(BeFalse())
	})
})
