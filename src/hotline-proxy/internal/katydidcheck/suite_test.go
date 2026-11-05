package katydidcheck_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestKatydidcheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Katydidcheck Suite")
}
