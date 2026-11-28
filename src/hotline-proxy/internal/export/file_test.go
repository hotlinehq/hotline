package export_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The file writer's answers to a folder that changes under it, which the
// proxy's scenarios cannot arrange at the right moment. A rotation that
// fails is checked inside the package, in file_internal_test.go, as no way
// in can make one fail.
var _ = Describe("Writing call lines to a folder", func() {
	sut := exportersSUT{}

	It("refuses a folder whose lock file cannot be made", func() {
		Expect(sut.StartErrorWithFolderInPlaceOfTheLock()).To(HaveOccurred())
	})
})
