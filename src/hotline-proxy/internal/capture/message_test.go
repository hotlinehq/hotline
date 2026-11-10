package capture_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/capture"
)

// framed is a message the way the extension sends it: 4 bytes of size,
// then the body.
func framed(body string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...) //nolint:gosec // short test bodies
}

func read(raw []byte) (capture.Hello, error) {
	return capture.ReadHello(bufio.NewReader(bytes.NewReader(raw)))
}

var _ = Describe("The first message of a connection", func() {
	It("reads a flow's destination and program", func() {
		hello, err := read(framed(`{"version":1,"role":"flow","transport":"tcp","host":"api.example.com","port":443,"pid":4242,"processName":"/usr/bin/curl"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(hello).To(Equal(capture.Hello{
			Version: 1, Role: capture.RoleFlow, Transport: "tcp", Host: "api.example.com", Port: 443, PID: 4242, ProcessName: "/usr/bin/curl",
		}))
	})

	It("reads a control hello, which names nothing else", func() {
		hello, err := read(framed(`{"version":1,"role":"control"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(hello.Role).To(Equal(capture.RoleControl))
	})

	It("leaves the bytes after the hello unread", func() {
		raw := append(framed(`{"version":1,"role":"flow","transport":"tcp","host":"a.example","port":443}`), "TLS"...)
		r := bufio.NewReader(bytes.NewReader(raw))
		_, err := capture.ReadHello(r)
		Expect(err).NotTo(HaveOccurred())
		rest := make([]byte, 3)
		_, err = r.Read(rest)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rest)).To(Equal("TLS"))
	})

	It("refuses a size over 16 KiB without reading the body", func() {
		raw := binary.BigEndian.AppendUint32(nil, capture.MaxFrame+1)
		_, err := read(raw)
		Expect(err).To(MatchError(capture.ErrBadHello))
	})

	DescribeTable("refuses what is not a usable hello",
		func(raw []byte) {
			_, err := read(raw)
			Expect(err).To(MatchError(capture.ErrBadHello))
		},
		Entry("nothing", []byte{}),
		Entry("a size cut short", []byte{0, 0}),
		Entry("a body cut short", append(binary.BigEndian.AppendUint32(nil, 50), "{}"...)),
		Entry("a body that is not JSON", framed("hello")),
		Entry("an unknown version", framed(`{"version":2,"role":"control"}`)),
		Entry("no version", framed(`{"role":"control"}`)),
		Entry("an unknown role", framed(`{"version":1,"role":"admin"}`)),
		Entry("a flow with no host", framed(`{"version":1,"role":"flow","transport":"tcp","port":443}`)),
		Entry("a flow with a host that has a slash", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a/b","port":443}`)),
		Entry("a flow with a host with a space", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a b","port":443}`)),
		Entry("a flow with a host over 253 characters", framed(`{"version":1,"role":"flow","transport":"tcp","host":"`+strings.Repeat("a", 254)+`","port":443}`)),
		Entry("a flow with port 0", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a.example","port":0}`)),
		Entry("a flow with a port over 65535", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a.example","port":65536}`)),
		Entry("a flow that is not TCP", framed(`{"version":1,"role":"flow","transport":"udp","host":"a.example","port":443}`)),
		Entry("a flow with a negative process number", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a.example","port":443,"pid":-1}`)),
		Entry("a flow with a program path over 4096 characters", framed(`{"version":1,"role":"flow","transport":"tcp","host":"a.example","port":443,"processName":"`+strings.Repeat("a", 4097)+`"}`)),
	)

	It("accepts an IPv6 address as the host", func() {
		hello, err := read(framed(`{"version":1,"role":"flow","transport":"tcp","host":"2001:db8::1","port":443}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(hello.Host).To(Equal("2001:db8::1"))
	})
})

var _ = Describe("The app list sent to the extension", func() {
	It("keeps the names and ends with a skip rule for Hotline itself", func() {
		Expect(capture.Spec([]string{"curl", "firefox"}, 99).Entries).To(Equal([]string{"curl", "firefox", "!99"}))
	})

	It("keeps a list that starts with a skip rule starting with it, so all else is captured", func() {
		Expect(capture.Spec([]string{"!slack"}, 99).Entries).To(Equal([]string{"!slack", "!99"}))
	})

	It("carries the protocol version", func() {
		Expect(capture.Spec([]string{"curl"}, 1).Version).To(Equal(capture.Version))
	})

	It("is written as a framed message the extension reads", func() {
		raw, err := capture.Frame(capture.Spec([]string{"curl"}, 7))
		Expect(err).NotTo(HaveOccurred())
		Expect(raw[4:]).To(MatchJSON(`{"version":1,"entries":["curl","!7"]}`))
		Expect(binary.BigEndian.Uint32(raw[:4])).To(BeEquivalentTo(len(raw) - 4))
	})
})
