package onetime_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOnetime(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Onetime Suite")
}
