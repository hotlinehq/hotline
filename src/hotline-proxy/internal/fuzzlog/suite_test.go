package fuzzlog_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFuzzlog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Fuzzlog Suite")
}
