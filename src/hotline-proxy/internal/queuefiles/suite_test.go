package queuefiles_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestQueuefiles(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Queuefiles Suite")
}
