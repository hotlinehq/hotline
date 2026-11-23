package stripe_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStripe(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stripe to OpenAPI 3.2 Suite")
}
