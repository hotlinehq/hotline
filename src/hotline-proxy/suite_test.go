package proxy_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestProxy runs the small tests: of the test world, the helper the feature
// files run against, and of parts of the proxy too small for a scenario.
func TestProxy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Proxy Suite")
}
