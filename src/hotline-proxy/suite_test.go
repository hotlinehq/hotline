package proxy_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestWorld runs the small tests of the test world itself, the helper the
// feature files run against.
func TestWorld(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Test world Suite")
}
