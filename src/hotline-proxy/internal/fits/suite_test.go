package fits_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFits(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Fits Suite")
}
