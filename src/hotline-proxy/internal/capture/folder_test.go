package capture_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/capture"
)

var _ = Describe("The private socket folder", func() {
	It("is made with mode 0700, owned by the current user, under a random name", func() {
		first, err := capture.NewFolder()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Close)
		second, err := capture.NewFolder()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(second.Close)

		info, err := os.Stat(filepath.Dir(first.Socket()))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		Expect(filepath.Dir(first.Socket())).NotTo(Equal(filepath.Dir(second.Socket())))
		Expect(capture.CheckFolder(filepath.Dir(first.Socket()), os.Getuid())).To(Succeed())
	})

	It("is removed with its socket when closed", func() {
		folder, err := capture.NewFolder()
		Expect(err).NotTo(HaveOccurred())
		dir := filepath.Dir(folder.Socket())
		Expect(os.WriteFile(folder.Socket(), nil, 0o600)).To(Succeed())
		Expect(folder.Close()).To(Succeed())
		Expect(dir).NotTo(BeADirectory())
	})

	Describe("the check at start", func() {
		var dir string
		BeforeEach(func() { dir = GinkgoT().TempDir() })

		It("accepts a folder with mode 0700 owned by the user", func() {
			Expect(os.Chmod(dir, 0o700)).To(Succeed()) //nolint:gosec // the mode the folder must have
			Expect(capture.CheckFolder(dir, os.Getuid())).To(Succeed())
		})

		DescribeTable("refuses a folder open to others",
			func(mode os.FileMode) {
				Expect(os.Chmod(dir, mode)).To(Succeed())
				err := capture.CheckFolder(dir, os.Getuid())
				Expect(err).To(MatchError(capture.ErrWrongMode))
			},
			Entry("group can read", os.FileMode(0o750)),
			Entry("everyone can read", os.FileMode(0o755)),
			Entry("group can write", os.FileMode(0o770)),
			Entry("stricter than needed, so the user cannot enter", os.FileMode(0o500)),
		)

		It("refuses a folder owned by someone else", func() {
			Expect(os.Chmod(dir, 0o700)).To(Succeed()) //nolint:gosec // the mode the folder must have
			err := capture.CheckFolder(dir, os.Getuid()+1)
			Expect(err).To(MatchError(capture.ErrWrongOwner))
		})

		It("refuses a path that is not a folder", func() {
			file := filepath.Join(dir, "file")
			Expect(os.WriteFile(file, nil, 0o600)).To(Succeed())
			Expect(capture.CheckFolder(file, os.Getuid())).To(MatchError(capture.ErrNotFolder))
		})

		It("refuses a link to a folder", func() {
			Expect(os.Chmod(dir, 0o700)).To(Succeed()) //nolint:gosec // the mode the folder must have
			link := filepath.Join(GinkgoT().TempDir(), "link")
			Expect(os.Symlink(dir, link)).To(Succeed())
			Expect(capture.CheckFolder(link, os.Getuid())).To(MatchError(capture.ErrNotFolder))
		})

		It("refuses a folder that is not there", func() {
			Expect(capture.CheckFolder(filepath.Join(dir, "gone"), os.Getuid())).NotTo(Succeed())
		})
	})
})
