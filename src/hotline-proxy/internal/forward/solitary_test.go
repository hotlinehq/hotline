package forward_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/forward"
)

var _ = Describe("Forwarding without a server", func() {
	sut := soloSUT{}

	It("ignores an early response that comes after the response", func() {
		sut.forVendorAnswering(http.StatusOK, http.Header{}, http.NoBody)
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.EarlyResponseAfterTheResponse(http.StatusEarlyHints, http.Header{"Link": {"</a.css>"}})).To(Succeed())
		Expect(sut.AppStatus()).To(Equal(http.StatusOK))
		Expect(sut.AppHeader()).NotTo(HaveKey("Link"))
	})

	It("adds the vendor's values to a header the response already has", func() {
		sut.forVendorAnswering(http.StatusOK, http.Header{"Vary": {"Accept"}}, http.NoBody)
		sut.AppHeader().Set("Vary", "Origin")
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.AppHeader()["Vary"]).To(Equal([]string{"Origin", "Accept"}))
	})

	It("stops quietly when the vendor breaks off, as there is no server to break the response", func() {
		sut.forVendorAnswering(http.StatusOK, http.Header{}, &brokenBody{})
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.AppBody()).To(Equal("part"))
	})

	It("stops when the app can no longer be written to", func() {
		sut.forVendorAnswering(http.StatusOK, http.Header{}, io.NopCloser(strings.NewReader("body")))
		sut.AppCannotBeWritten()
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.AppBody()).To(BeEmpty())
	})

	It("stops when a part of a stream cannot be flushed to the app", func() {
		sut.forVendorSending(-1, http.Header{}, "body")
		sut.AppCannotBeFlushed()
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.AppBody()).To(Equal("body"))
	})

	// Flushing sends a network write of its own, so the headers of a response
	// of known length go with its first part, as Go's own ReverseProxy does.
	It("sends the headers of a response of known length with its first part", func() {
		sut.forVendorSending(4, http.Header{}, "body")
		sut.AppCountsFlushes()
		Expect(sut.Forward()).To(Succeed())
		Expect(sut.AppBody()).To(Equal("body"))
		Expect(sut.Flushes()).To(Equal(1))
	})

	DescribeTable("flushes the headers and every part of a stream",
		func(length int64, contentType string) {
			sut.forVendorSending(length, http.Header{"Content-Type": {contentType}}, "body")
			sut.AppCountsFlushes()
			Expect(sut.Forward()).To(Succeed())
			Expect(sut.Flushes()).To(Equal(2))
		},
		Entry("a response of unknown length", int64(-1), "application/json"),
		Entry("events, even with a known length", int64(4), "text/event-stream; charset=utf-8"),
	)
})

var _ = Describe("Upgrades without a server", func() {
	sut := soloSUT{}

	It("refuses a switch when the vendor's connection cannot be written to", func() {
		sut.forVendorSwitching(http.NoBody)
		Expect(sut.ForwardUpgrade()).NotTo(Succeed())
	})

	It("returns an error when the app's connection cannot be taken over", func() {
		sut.forVendorSwitching(pipeBody{strings.NewReader("")})
		Expect(sut.ForwardUpgrade()).NotTo(Succeed())
	})

	It("stops when the app has gone before the switch reaches it", func() {
		sut.forVendorSwitching(pipeBody{strings.NewReader("")})
		sut.AppGoneBeforeTheSwitch()
		Expect(sut.ForwardUpgrade()).To(Succeed())
		Expect(sut.SwitchesTold()).To(BeZero())
	})

	It("tells the writer once the app has the 101, before any byte passes after it", func() {
		sut.forVendorSwitching(pipeBody{strings.NewReader("after")})
		Expect(sut.ForwardUpgradeAndReadAsApp()).To(Equal("101 1 after"))
	})
})

// soloSUT is a forwarder with no server around it: a fake vendor that
// answers without a network, and the app's side, a plain recorder unless
// the test breaks it or counts its flushes.
type soloSUT struct {
	vendor   fakeVendor
	recorder *httptest.ResponseRecorder
	app      *appWriter
	// trace is how the forwarder told the vendor's call what happens on
	// the way, kept once the vendor got the call.
	trace *httptrace.ClientTrace
}

// forVendorAnswering is a vendor that answers status, header and body.
func (s *soloSUT) forVendorAnswering(status int, header http.Header, body io.ReadCloser) {
	s.recorder, s.app = httptest.NewRecorder(), nil
	s.vendor = func(out *http.Request) *http.Response {
		s.trace = httptrace.ContextClientTrace(out.Context())
		return &http.Response{StatusCode: status, Header: header, Body: body}
	}
}

// forVendorSending is a vendor that answers 200 with a body of the
// announced length, -1 for unknown.
func (s *soloSUT) forVendorSending(length int64, header http.Header, body string) {
	s.forVendorAnswering(http.StatusOK, header, io.NopCloser(strings.NewReader(body)))
	answer := s.vendor
	s.vendor = func(out *http.Request) *http.Response {
		res := answer(out)
		res.ContentLength = length
		return res
	}
}

// forVendorSwitching is a vendor that switches to WebSocket, with
// connection as what it gives after the switch.
func (s *soloSUT) forVendorSwitching(connection io.ReadCloser) {
	s.forVendorAnswering(http.StatusSwitchingProtocols, http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}, connection)
}

// AppCountsFlushes makes the app's side one that counts its flushes and
// can be taken over.
func (s *soloSUT) AppCountsFlushes() {
	if s.app == nil {
		s.app = &appWriter{ResponseRecorder: s.recorder}
	}
}

// AppCannotBeWritten makes every write to the app fail.
func (s *soloSUT) AppCannotBeWritten() {
	s.AppCountsFlushes()
	s.app.writeErr = errAppGone
}

// AppCannotBeFlushed makes every flush to the app fail.
func (s *soloSUT) AppCannotBeFlushed() {
	s.AppCountsFlushes()
	s.app.flushErr = errAppGone
}

// AppGoneBeforeTheSwitch gives the app a connection whose other end has
// closed.
func (s *soloSUT) AppGoneBeforeTheSwitch() {
	app, other := net.Pipe()
	Expect(other.Close()).WithOffset(1).To(Succeed())
	s.AppCountsFlushes()
	s.app.conn = app
}

// Forward forwards the app's GET call.
func (s *soloSUT) Forward() error { return s.forward(s.appCall()) }

// ForwardUpgrade forwards the app's call asking to switch to WebSocket.
func (s *soloSUT) ForwardUpgrade() error {
	in := s.appCall()
	in.Header.Set("Connection", "Upgrade")
	in.Header.Set("Upgrade", "websocket")
	return s.forward(in)
}

// ForwardUpgradeAndReadAsApp forwards the app's upgrade over a connection
// it reads as the app: the status of the answer, how often the writer had
// been told of the switch when the first byte after it came, and the bytes
// after the switch.
func (s *soloSUT) ForwardUpgradeAndReadAsApp() string {
	app, other := net.Pipe()
	s.AppCountsFlushes()
	s.app.conn = app
	read := make(chan string, 1)
	go func() {
		br := bufio.NewReader(other)
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			read <- err.Error()
			return
		}
		_ = res.Body.Close()
		first, _ := br.ReadByte()
		switches := s.SwitchesTold()
		rest, _ := io.ReadAll(br)
		read <- fmt.Sprintf("%d %d %c%s", res.StatusCode, switches, first, rest)
	}()
	Expect(s.ForwardUpgrade()).WithOffset(1).To(Succeed())
	Expect(other.Close()).WithOffset(1).To(Succeed())
	var got string
	Eventually(read).WithOffset(1).Should(Receive(&got))
	return got
}

// EarlyResponseAfterTheResponse tells the forwarder of an early response
// that came after the vendor's response.
func (s *soloSUT) EarlyResponseAfterTheResponse(status int, header http.Header) error {
	return s.trace.Got1xxResponse(status, textproto.MIMEHeader(header))
}

// AppStatus is the status the app got.
func (s *soloSUT) AppStatus() int { return s.recorder.Code }

// AppHeader is the header of the app's response.
func (s *soloSUT) AppHeader() http.Header { return s.recorder.Header() }

// AppBody is the body the app got.
func (s *soloSUT) AppBody() string { return s.recorder.Body.String() }

// Flushes is how often the forwarder flushed to the app.
func (s *soloSUT) Flushes() int { return s.app.flushes }

// SwitchesTold is how often the forwarder told the writer of the switch.
func (s *soloSUT) SwitchesTold() int {
	if s.app == nil {
		return 0
	}
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	return s.app.switches
}

func (*soloSUT) appCall() *http.Request {
	return httptest.NewRequestWithContext(GinkgoT().Context(), http.MethodGet, "http://proxy.test/x", nil)
}

func (s *soloSUT) forward(in *http.Request) error {
	var w http.ResponseWriter = s.recorder
	if s.app != nil {
		w = s.app
	}
	return (&forward.Forwarder{Buffers: &buffers{}}).Forward(GinkgoT().Context(), w, in, s.vendor)
}

// fakeVendor answers every call with what response gives, without a network.
type fakeVendor func(out *http.Request) *http.Response

func (v fakeVendor) RoundTrip(out *http.Request) (*http.Response, error) {
	res := v(out)
	res.Request = out
	return res, nil
}

var (
	errVendorGone = errors.New("vendor went away")
	errAppGone    = errors.New("app went away")
)

// brokenBody gives some bytes, then fails.
type brokenBody struct{ sent bool }

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, errVendorGone
	}
	b.sent = true
	return copy(p, "part"), nil
}
func (*brokenBody) Close() error { return nil }

// pipeBody is an upgraded vendor connection that nobody writes to.
type pipeBody struct{ io.Reader }

func (pipeBody) Write(p []byte) (int, error) { return len(p), nil }
func (pipeBody) Close() error                { return nil }

// appWriter is the app's side, with a choice of what breaks.
type appWriter struct {
	*httptest.ResponseRecorder
	writeErr, flushErr error
	conn               net.Conn
	flushes            int

	mu       sync.Mutex
	switches int
}

func (a *appWriter) Switched() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.switches++
}

func (a *appWriter) Write(p []byte) (int, error) {
	if a.writeErr != nil {
		return 0, a.writeErr
	}
	return a.ResponseRecorder.Write(p)
}

func (a *appWriter) FlushError() error {
	a.flushes++
	return a.flushErr
}

func (a *appWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return a.conn, bufio.NewReadWriter(bufio.NewReader(a.conn), bufio.NewWriter(a.conn)), nil
}
