//go:build unix

package fuzzlog_test

import (
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/unix"

	"hotline-proxy/internal/fuzzlog"
)

var _ = Describe("capturing a fuzz worker's output", func() {
	var dir string
	var args []string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		// Pretend this test process is a fuzz worker, and put its standard
		// error back once the test is done.
		args = os.Args
		os.Args = append([]string{args[0], "-test.fuzzworker"}, args[1:]...)
		saved, err := unix.Dup(int(os.Stderr.Fd()))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			os.Args = args
			Expect(unix.Dup2(saved, int(os.Stderr.Fd()))).To(Succeed())
			Expect(unix.Close(saved)).To(Succeed())
		})
	})

	It("writes what the worker writes to its standard error to its own file", func() {
		GinkgoT().Setenv(fuzzlog.DirVariable, dir)
		fuzzlog.Capture()
		_, err := os.Stderr.WriteString("panic: boom\n")
		Expect(err).NotTo(HaveOccurred())

		Expect(os.ReadFile(filepath.Join(dir, "worker-"+strconv.Itoa(os.Getpid())+".log"))).To(Equal([]byte("panic: boom\n")))
	})

	It("leaves the output where it was when the folder cannot take the file", func() {
		GinkgoT().Setenv(fuzzlog.DirVariable, filepath.Join(dir, "missing"))
		fuzzlog.Capture()

		Expect(filepath.Join(dir, "missing")).NotTo(BeAnExistingFile())
	})

	It("does nothing in a process that is not a fuzz worker", func() {
		os.Args = args
		GinkgoT().Setenv(fuzzlog.DirVariable, dir)
		fuzzlog.Capture()

		Expect(os.ReadDir(dir)).To(BeEmpty())
	})
})
