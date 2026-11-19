package live_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLive(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Live session Suite")
}
