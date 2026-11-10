package capture_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/capture"
)

var _ = Describe("The apps to capture", func() {
	DescribeTable("accepts names, process numbers and skip rules",
		func(apps ...string) { Expect(capture.CheckApps(apps)).To(Succeed()) },
		Entry("a name", "curl"),
		Entry("several", "curl", "firefox"),
		Entry("a process number", "4242"),
		Entry("a skip rule", "!slack"),
		Entry("a path piece", "bin/curl"),
	)

	DescribeTable("refuses what cannot be matched",
		func(apps ...string) { Expect(capture.CheckApps(apps)).To(MatchError(capture.ErrBadApp)) },
		Entry("a skip mark alone", "!"),
		Entry("an empty name", ""),
		Entry("a space in the name", "my app"),
		Entry("a control character", "cu\trl"),
	)

	It("refuses an empty list, which would capture nothing", func() {
		Expect(capture.CheckApps(nil)).To(MatchError(capture.ErrNoApps))
	})
})

var _ = Describe("The address the extension connects to", func() {
	DescribeTable("accepts a loopback address",
		func(address string) { Expect(capture.CheckAddress(address)).To(Succeed()) },
		Entry("IPv4", "127.0.0.1:8444"),
		Entry("another IPv4 loopback", "127.5.5.5:8444"),
		Entry("IPv6", "[::1]:8444"),
		Entry("localhost", "localhost:8444"),
	)

	DescribeTable("refuses an address others could reach",
		func(address string) { Expect(capture.CheckAddress(address)).To(MatchError(capture.ErrNotLoopback)) },
		Entry("all interfaces", "0.0.0.0:8444"),
		Entry("no host, which means all interfaces", ":8444"),
		Entry("a private network address", "10.0.0.5:8444"),
		Entry("a host name", "proxy.example.com:8444"),
		Entry("IPv6 for all interfaces", "[::]:8444"),
		Entry("not an address at all", "nonsense"),
	)
})
