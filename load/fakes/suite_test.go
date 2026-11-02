package fakes_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFakes(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Load test fakes Suite")
}
