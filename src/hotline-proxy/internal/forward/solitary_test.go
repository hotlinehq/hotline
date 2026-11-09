package forward_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/forward"
)

// fakeVendor answers every call with what reply gives, without a network.
type fakeVendor func(out *http.Request) *http.Response

func (v fakeVendor) RoundTrip(out *http.Request) (*http.Response, error) {
	res := v(out)
	res.Request = out
	return res, nil
}

func reply(status int, header http.Header, body io.ReadCloser) fakeVendor {
	return func(*http.Request) *http.Response {
		return &http.Response{StatusCode: status, Header: header, Body: body}
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
}

func (a *appWriter) Write(p []byte) (int, error) {
	if a.writeErr != nil {
		return 0, a.writeErr
	}
	return a.ResponseRecorder.Write(p)
}

func (a *appWriter) FlushError() error { return a.flushErr }

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
	It("ignores an early reply that comes after the reply", func() {
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

	It("adds the vendor's values to a header the reply already has", func() {
		f := forwarder(reply(http.StatusOK, http.Header{"Vary": {"Accept"}}, http.NoBody))
		w := httptest.NewRecorder()
		w.Header().Set("Vary", "Origin")
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Header()["Vary"]).To(Equal([]string{"Origin", "Accept"}))
	})

	It("stops quietly when the vendor breaks off, as there is no server to break the reply", func() {
		f := forwarder(reply(http.StatusOK, http.Header{}, &brokenBody{}))
		w := httptest.NewRecorder()
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(Equal("part"))
	})

	It("stops when the app can no longer be written to", func() {
		f := forwarder(reply(http.StatusOK, http.Header{}, io.NopCloser(strings.NewReader("body"))))
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: errAppGone}
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(BeEmpty())
	})

	It("stops when a part cannot be flushed to the app", func() {
		f := forwarder(reply(http.StatusOK, http.Header{}, io.NopCloser(strings.NewReader("body"))))
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: errAppGone}
		Expect(f.Forward(GinkgoT().Context(), w, appCall())).To(Succeed())
		Expect(w.Body.String()).To(Equal("body"))
	})
})

var _ = Describe("Upgrades without a server", func() {
	It("refuses a switch when the vendor's connection cannot be written to", func() {
		f := forwarder(reply(http.StatusSwitchingProtocols, switched(), http.NoBody))
		Expect(f.Forward(GinkgoT().Context(), httptest.NewRecorder(), upgradeCall())).NotTo(Succeed())
	})

	It("returns an error when the app's connection cannot be taken over", func() {
		f := forwarder(reply(http.StatusSwitchingProtocols, switched(), pipeBody{strings.NewReader("")}))
		Expect(f.Forward(GinkgoT().Context(), httptest.NewRecorder(), upgradeCall())).NotTo(Succeed())
	})

	It("stops when the app has gone before the switch reaches it", func() {
		f := forwarder(reply(http.StatusSwitchingProtocols, switched(), pipeBody{strings.NewReader("")}))
		app, other := net.Pipe()
		Expect(other.Close()).To(Succeed())
		w := &appWriter{ResponseRecorder: httptest.NewRecorder(), conn: app}
		Expect(f.Forward(GinkgoT().Context(), w, upgradeCall())).To(Succeed())
	})
})
