package export_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/export"
)

// The file writer's answers to a folder that changes under it, which the
// proxy's scenarios cannot arrange at the right moment.
var _ = Describe("Writing call lines to a folder", func() {
	var dir string
	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("refuses a folder whose lock file cannot be made", func() {
		Expect(os.Mkdir(filepath.Join(dir, ".lock"), 0o750)).To(Succeed())
		_, err := export.OpenTestFileWriter(dir, 1<<20, 2)
		Expect(err).To(HaveOccurred())
	})

	It("loses only the lines of a failed rotation, and writes on after it", func() {
		w, err := export.OpenTestFileWriter(dir, 1<<20, 2)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = w.Close() }()
		Expect(w.Write(`{"call":1}`)).To(Succeed())
		Expect(os.Remove(filepath.Join(dir, "calls.jsonl"))).To(Succeed())

		Expect(w.Rotate()).To(HaveOccurred())
		Expect(w.Write(`{"call":2}`)).To(Succeed())

		Expect(os.ReadFile(filepath.Join(dir, "calls.jsonl"))).To(Equal([]byte("{\"call\":2}\n"))) //nolint:gosec // a folder the test made
	})
})
