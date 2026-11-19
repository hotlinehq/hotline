package forward_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/forward"
)

// fakeVendor answers every call with what response gives, without a network.
type fakeVendor func(out *http.Request) *http.Response

func (v fakeVendor) RoundTrip(out *http.Request) (*http.Response, error) {
	res := v(out)
	res.Request = out
	return res, nil
}

func response(status int, header http.Header, body io.ReadCloser) fakeVendor {
	return func(*http.Request) *http.Response {
		return &http.Response{StatusCode: status, Header: header, Body: body}
	}
}

// sized answers with a body of the given announced length, -1 for unknown.
func sized(length int64, header http.Header, body string) fakeVendor {
	return func(*http.Request) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: length,
			Body: io.NopCloser(strings.NewReader(body))}
	}
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

func (a *appWriter) switchCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.switches
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

// solo forwards each call to t.
type solo struct{ t http.RoundTripper }

func forwarder(t http.RoundTripper) solo { return solo{t} }

func (f solo) Forward(ctx context.Context, w http.ResponseWriter, in *http.Request) error {
	return (&forward.Forwarder{Buffers: &buffers{}}).Forward(ctx, w, in, f.t)
}

func appCall() *http.Request {
	return httptest.NewRequestWithContext(GinkgoT().Context(), http.MethodGet, "http://proxy.test/x", nil)
}

func upgradeCall() *http.Request {
	in := appCall()
	in.Header.Set("Connection", "Upgrade")
	in.Header.Set("Upgrade", "websocket")
	return in
}

func switched() http.Header {
	return http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}}
}

var _ = Describe("Forwarding without a server", func() {
	It("ignores an early response that comes after the response", func() {
		var trace *httptrace.ClientTrace
		f := forwarder(fakeVendor(func(out *http.Request) *http.Response {
			trace = httptrace.ContextClientTrace(out.Context())
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}
		}))
		w := httptest.NewRecorder()
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(trace.Got1xxResponse(http.StatusEarlyHints, map[string][]string{"Link": {"</a.css>"}})).To(Succeed())
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header()).NotTo(HaveKey("Link"))
	})

	It("adds the vendor's values to a header the response already has", func() {
		f := forwarder(response(http.StatusOK, http.Header{"Vary": {"Accept"}}, http.NoBody))
		w := httptest.NewRecorder()
		w.Header().Set("Vary", "Origin")
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Header()["Vary"]).To(Equal([]string{"Origin", "Accept"}))
	})

	It("stops quietly when the vendor breaks off, as there is no server to break the response", func() {
		f := forwarder(response(http.StatusOK, http.Header{}, &brokenBody{}))
		w := httptest.NewRecorder()
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(Equal("part"))
	})

	It("stops when the app can no longer be written to", func() {
		f := forwarder(response(http.StatusOK, http.Header{}, io.NopCloser(strings.NewReader("body"))))
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: errAppGone}
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(BeEmpty())
	})

	It("stops when a part of a stream cannot be flushed to the app", func() {
		f := forwarder(sized(-1, http.Header{}, "body"))
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: errAppGone}
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(Equal("body"))
	})

	// Flushing sends a network write of its own, so a response of known length
	// goes out in one go, as Go's own ReverseProxy does.
	// Flushing sends a network write of its own, so the headers of a response
	// of known length go with its first part, as Go's own ReverseProxy does.
	It("sends the headers of a response of known length with its first part", func() {
		f := forwarder(sized(4, http.Header{}, "body"))
		w := &appWriter{ResponseRecorder: httptest.NewRecorder()}
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(Equal("body"))
		Expect(w.flushes).To(Equal(1))
	})

	DescribeTable("flushes the headers and every part of a stream",
		func(length int64, contentType string) {
			f := forwarder(sized(length, http.Header{"Content-Type": {contentType}}, "body"))
			w := &appWriter{ResponseRecorder: httptest.NewRecorder()}
			Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
			Expect(w.flushes).To(Equal(2))
		},
		Entry("a response of unknown length", int64(-1), "application/json"),
		Entry("events, even with a known length", int64(4), "text/event-stream; charset=utf-8"),
	)
})

var _ = Describe("Upgrades without a server", func() {
	It("refuses a switch when the vendor's connection cannot be written to", func() {
		f := forwarder(response(http.StatusSwitchingProtocols, switched(), http.NoBody))
		Expect(f.Forward(GinkgoT().Context(), httptest.NewRecorder(), upgradeCall())).NotTo(Succeed())
	})

	It("returns an error when the app's connection cannot be taken over", func() {
		f := forwarder(response(http.StatusSwitchingProtocols, switched(), pipeBody{strings.NewReader("")}))
		Expect(f.Forward(GinkgoT().Context(), httptest.NewRecorder(), upgradeCall())).NotTo(Succeed())
	})

	It("stops when the app has gone before the switch reaches it", func() {
		f := forwarder(response(http.StatusSwitchingProtocols, switched(), pipeBody{strings.NewReader("")}))
		app, other := net.Pipe()
		Expect(other.Close()).To(Succeed())
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), conn: app}
		Expect(f.Forward(GinkgoT().Context(), w, upgradeCall())).To(Succeed())
		Expect(w.switches).To(BeZero())
	})

	It("tells the writer once the app has the 101, before any byte passes after it", func() {
		f := forwarder(response(http.StatusSwitchingProtocols, switched(), pipeBody{strings.NewReader("after")}))
		app, other := net.Pipe()
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), conn: app}
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
			switches := w.switchCount()
			rest, _ := io.ReadAll(br)
			read <- fmt.Sprintf("%d %d %c%s", res.StatusCode, switches, first, rest)
		}()
		Expect(f.Forward(GinkgoT().Context(), w, upgradeCall())).To(Succeed())
		Expect(other.Close()).To(Succeed())
		Eventually(read).Should(Receive(Equal("101 1 after")))
	})
})
