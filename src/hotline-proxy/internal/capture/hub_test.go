package capture_test

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/capture"
)

// readList reads one app list message from the extension's side of a pipe.
func readList(conn net.Conn) []string {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	size := make([]byte, 4)
	_, err := io.ReadFull(conn, size)
	Expect(err).NotTo(HaveOccurred())
	body := make([]byte, binary.BigEndian.Uint32(size))
	_, err = io.ReadFull(conn, body)
	Expect(err).NotTo(HaveOccurred())
	var list capture.SpecUpdate
	Expect(json.Unmarshal(body, &list)).To(Succeed())
	return list.Entries
}

var _ = Describe("The hub of control connections", func() {
	It("sends the app list, then every new list, to a joined connection", func() {
		hub := capture.NewHub([]string{"curl"})
		proxySide, extensionSide := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- hub.Serve(proxySide, bufio.NewReader(proxySide)) }()

		Expect(readList(extensionSide)).To(HaveLen(2))

		go hub.Update([]string{"curl", "firefox"})
		Expect(readList(extensionSide)).To(HaveLen(3))

		_ = extensionSide.Close()
		Eventually(done).Should(Receive())
	})

	It("closes a connection that cannot take the new list", func() {
		hub := capture.NewHub([]string{"curl"})
		conn := newBrokenWriter()
		go func() { _ = hub.Serve(conn, conn) }()
		Eventually(conn.joined).Should(BeClosed())

		hub.Update([]string{"firefox"})
		Eventually(conn.closed).Should(BeClosed())
	})

	It("keeps the old list when the new one is too large to send", func() {
		hub := capture.NewHub([]string{"curl"})
		hub.Update([]string{strings.Repeat("a", capture.MaxFrame)})
		proxySide, extensionSide := net.Pipe()
		go func() { _ = hub.Serve(proxySide, bufio.NewReader(proxySide)) }()
		Expect(readList(extensionSide)).To(ContainElement("curl"))
		_ = extensionSide.Close()
	})

	It("refuses a new control connection once closed", func() {
		hub := capture.NewHub([]string{"curl"})
		hub.Close()
		proxySide, extensionSide := net.Pipe()
		defer func() { _ = extensionSide.Close() }()
		Expect(hub.Serve(proxySide, bufio.NewReader(proxySide))).To(MatchError(capture.ErrHubClosed))
	})

	It("ends a joined connection when the hub is closed", func() {
		hub := capture.NewHub([]string{"curl"})
		proxySide, extensionSide := net.Pipe()
		done := make(chan error, 1)
		go func() { done <- hub.Serve(proxySide, bufio.NewReader(proxySide)) }()
		readList(extensionSide)

		hub.Close()
		Eventually(done).Should(Receive(BeNil()))
		_ = extensionSide.Close()
	})

	It("says so when the extension cannot take the first list", func() {
		hub := capture.NewHub([]string{"curl"})
		proxySide, extensionSide := net.Pipe()
		_ = extensionSide.Close()
		Expect(hub.Serve(proxySide, bufio.NewReader(proxySide))).To(HaveOccurred())
	})
})

// brokenWriter takes the first write, as a joining extension would, refuses
// every write after it, and blocks reads until it is closed.
type brokenWriter struct {
	net.Conn
	writes int
	joined chan struct{}
	closed chan struct{}
}

func newBrokenWriter() *brokenWriter {
	return &brokenWriter{joined: make(chan struct{}), closed: make(chan struct{})}
}

func (c *brokenWriter) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == 1 {
		close(c.joined)
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}

func (c *brokenWriter) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.ErrClosedPipe
}

func (c *brokenWriter) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *brokenWriter) SetDeadline(time.Time) error      { return nil }
func (c *brokenWriter) SetWriteDeadline(time.Time) error { return nil }
