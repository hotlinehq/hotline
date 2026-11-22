package pathprobe_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPathprobe(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Pathprobe Suite")
}
