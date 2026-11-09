package forward_test

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/forward"
)

type buffers struct{ p sync.Pool }

func (b *buffers) Get() []byte {
	if buf, ok := b.p.Get().(*[]byte); ok {
		return *buf
	}
	return make([]byte, 32<<10)
}
func (b *buffers) Put(buf []byte) { b.p.Put(&buf) }

// rig is an app's proxy with a forwarder in front of one vendor.
type rig struct {
	vendor, proxy *httptest.Server
	// calls is how long each call's context lasts; 0 is until the handler
	// ends.
	calls time.Duration

	mu sync.Mutex
	// before and after are the app's request headers when the call came in
	// and once it was forwarded.
	before, after http.Header
}

func newRig(vendor http.Handler, configure ...func(*rig)) *rig {
	r := &rig{}
	for _, c := range configure {
		c(r)
	}
	r.vendor = httptest.NewUnstartedServer(vendor)
	r.vendor.EnableHTTP2 = true
	r.vendor.StartTLS()
	u, _ := url.Parse(r.vendor.URL)
	transport := r.vendor.Client().Transport.(*http.Transport).Clone()
	transport.DisableCompression = true
	f := &forward.Forwarder{
		Transport: transport,
		Rewrite: func(out *http.Request) {
			target := *u
			target.Path, target.RawPath, target.RawQuery = out.URL.Path, out.URL.RawPath, out.URL.RawQuery
			out.URL = &target
			out.Host = "api.vendor.test"
			out.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		},
		Buffers: &buffers{},
	}
	r.proxy = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		before := in.Header.Clone()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(in.Context()), cmp.Or(r.calls, time.Minute))
		defer cancel()
		if err := f.Forward(ctx, w, in); err != nil {
			w.WriteHeader(http.StatusBadGateway)
		}
		r.mu.Lock()
		r.before, r.after = before, in.Header.Clone()
		r.mu.Unlock()
	}))
	r.proxy.EnableHTTP2 = true
	r.proxy.Start()
	DeferCleanup(func() { r.proxy.Close(); r.vendor.Close() })
	return r
}

// newHTTP1Rig is a rig whose vendor and app speak HTTP/1.1 only, as an
// upgrade needs, and whose calls end after calls when it is not 0.
func newHTTP1Rig(vendor http.Handler, calls time.Duration) *rig {
	r := &rig{calls: calls}
	v := httptest.NewServer(vendor)
	u, _ := url.Parse(v.URL)
	f := &forward.Forwarder{
		Transport: &http.Transport{},
		Rewrite:   func(out *http.Request) { out.URL.Scheme, out.URL.Host = u.Scheme, u.Host },
		Buffers:   &buffers{},
	}
	r.vendor = v
	r.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(in.Context()), cmp.Or(r.calls, time.Minute))
		defer cancel()
		if err := f.Forward(ctx, w, in); err != nil {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	DeferCleanup(func() { r.proxy.Close(); v.Close() })
	return r
}

func (r *rig) app() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// raw sends text straight to the proxy port and reads the first answer.
func (r *rig) raw(text string) (net.Conn, *bufio.Reader, *http.Response) {
	c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", strings.TrimPrefix(r.proxy.URL, "http://"))
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(c.Close)
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(c, text)
	Expect(err).NotTo(HaveOccurred())
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, nil)
	Expect(err).NotTo(HaveOccurred())
	return c, br, res
}

var _ = Describe("Forwarding a call", func() {
	It("leaves the app's own headers whole", func() {
		r := newRig(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req, _ := http.NewRequest(http.MethodGet, r.proxy.URL, nil)
		req.Header.Set("Connection", "X-Drop")
		req.Header.Set("X-Drop", "1")
		req.Header.Set("Te", "trailers")
		req.Header.Add("X-Keep", "a")
		req.Header.Add("X-Keep", "b")
		res, err := r.app().Do(req)
		Expect(err).NotTo(HaveOccurred())
		_ = res.Body.Close()
		Eventually(func() http.Header { r.mu.Lock(); defer r.mu.Unlock(); return r.after }).ShouldNot(BeNil())
		r.mu.Lock()
		defer r.mu.Unlock()
		Expect(r.after).To(Equal(r.before))
		Expect(r.after.Get("X-Drop")).To(Equal("1"))
	})

	It("sends what is for one hop only no further, either way", func() {
		var got http.Header
		r := newHTTP1Rig(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			got = q.Header.Clone()
			w.Header().Set("Connection", "X-Secret")
			w.Header().Set("X-Secret", "hidden")
			w.Header().Add("X-Multi", "a")
			w.Header().Add("X-Multi", "b")
		}), 0)
		req, _ := http.NewRequest(http.MethodGet, r.proxy.URL, nil)
		req.Header.Set("Connection", "X-Drop")
		req.Header.Set("X-Drop", "x")
		req.Header.Set("Proxy-Authorization", "Basic abc")
		req.Header.Set("X-Forwarded-For", "10.0.0.1")
		req.Header.Set("Te", "trailers")
		res, err := r.app().Do(req)
		Expect(err).NotTo(HaveOccurred())
		_ = res.Body.Close()
		Expect(got).NotTo(HaveKey("X-Drop"))
		Expect(got).NotTo(HaveKey("Proxy-Authorization"))
		Expect(got).NotTo(HaveKey("X-Forwarded-For"))
		Expect(got.Get("Te")).To(Equal("trailers"))
		Expect(res.Header).NotTo(HaveKey("X-Secret"))
		Expect(res.Header["X-Multi"]).To(Equal([]string{"a", "b"}))
	})

	It("adds no User-Agent of its own", func() {
		var got []string
		seen := false
		r := newRig(http.HandlerFunc(func(_ http.ResponseWriter, q *http.Request) { got, seen = q.Header["User-Agent"], true }))
		req, _ := http.NewRequest(http.MethodGet, r.proxy.URL, nil)
		req.Header["User-Agent"] = nil
		res, err := r.app().Do(req)
		Expect(err).NotTo(HaveOccurred())
		_ = res.Body.Close()
		Expect(seen).To(BeTrue())
		Expect(got).To(BeEmpty())
	})

	DescribeTable("passes bodies on whole",
		func(body io.Reader, length int64, want int) {
			var got []byte
			r := newRig(http.HandlerFunc(func(_ http.ResponseWriter, q *http.Request) { got, _ = io.ReadAll(q.Body) }))
			req, _ := http.NewRequest(http.MethodPost, r.proxy.URL, body)
			req.ContentLength = length
			res, err := r.app().Do(req)
			Expect(err).NotTo(HaveOccurred())
			_ = res.Body.Close()
			Expect(got).To(HaveLen(want))
		},
		Entry("with a length", strings.NewReader("hello"), int64(5), 5),
		Entry("empty", nil, int64(0), 0),
		Entry("in chunks", io.MultiReader(strings.NewReader("ab"), strings.NewReader("cd")), int64(-1), 4),
		Entry("of a megabyte", strings.NewReader(strings.Repeat("x", 1<<20)), int64(1<<20), 1<<20),
	)

	It("stops reading the app's body before it returns, when the vendor answers early", func() {
		r := newRig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}))
		for range 10 {
			pr, pw := io.Pipe()
			go func() {
				for range 64 {
					if _, err := pw.Write(make([]byte, 16<<10)); err != nil {
						return
					}
				}
				_ = pw.Close()
			}()
			req, _ := http.NewRequest(http.MethodPost, r.proxy.URL, pr)
			req.ContentLength = 1 << 20
			if res, err := r.app().Do(req); err == nil {
				Expect(res.StatusCode).To(Equal(http.StatusRequestEntityTooLarge))
				_ = res.Body.Close()
			}
			_ = pr.Close()
		}
	})

	It("passes a streamed reply on part by part", func() {
		release := make(chan struct{})
		r := newRig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "data: 1\n\n")
			w.(http.Flusher).Flush()
			<-release
		}))
		defer close(release)
		res, err := r.app().Get(r.proxy.URL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = res.Body.Close() }()
		line, err := bufio.NewReader(res.Body).ReadString('\n')
		Expect(err).NotTo(HaveOccurred())
		Expect(line).To(Equal("data: 1\n"))
	})

	It("breaks the app's reply when the vendor breaks off mid reply", func() {
		r := newRig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "part")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}))
		res, err := r.app().Get(r.proxy.URL)
		Expect(err).NotTo(HaveOccurred())
		_, err = io.ReadAll(res.Body)
		Expect(err).To(HaveOccurred())
	})

	It("returns an error when no reply comes, for the caller to answer", func() {
		r := newRig(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		r.vendor.Close()
		res, err := r.app().Get(r.proxy.URL)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.StatusCode).To(Equal(http.StatusBadGateway))
	})
})

var _ = Describe("Trailers", func() {
	It("passes the values of the app's trailers on", func() {
		got := make(chan string, 1)
		r := newRig(http.HandlerFunc(func(_ http.ResponseWriter, q *http.Request) {
			_, _ = io.Copy(io.Discard, q.Body)
			got <- q.Trailer.Get("X-Checksum")
		}))
		pr, pw := io.Pipe()
		req, _ := http.NewRequest(http.MethodPut, r.proxy.URL, pr)
		req.Trailer = http.Header{"X-Checksum": nil}
		go func() {
			_, _ = io.WriteString(pw, "upload")
			req.Trailer.Set("X-Checksum", "crc32=3610a686")
			_ = pw.Close()
		}()
		res, err := r.app().Do(req)
		Expect(err).NotTo(HaveOccurred())
		_ = res.Body.Close()
		Expect(<-got).To(Equal("crc32=3610a686"))
	})

	DescribeTable("passes the vendor's trailers back, announced or not",
		func(announce bool) {
			r := newRig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if announce {
					w.Header().Set("Trailer", "Grpc-Status")
				}
				_, _ = io.WriteString(w, "messages")
				w.(http.Flusher).Flush()
				w.Header().Set(http.TrailerPrefix+"Grpc-Status", "5")
			}))
			res, err := r.app().Get(r.proxy.URL)
			Expect(err).NotTo(HaveOccurred())
			_, _ = io.ReadAll(res.Body)
			Expect(res.Trailer.Get("Grpc-Status")).To(Equal("5"))
		},
		Entry("announced", true),
		Entry("not announced", false),
	)

	It("drops fields that must not come after the body", func() {
		// Over HTTP/1.1: Go's HTTP/2 server will not send such trailers.
		r := newHTTP1Rig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "body")
			w.(http.Flusher).Flush()
			w.Header().Set(http.TrailerPrefix+"X-Sum", "7")
			w.Header().Set(http.TrailerPrefix+"Content-Type", "text/evil")
			w.Header().Set(http.TrailerPrefix+"Www-Authenticate", "Basic")
		}), 0)
		res, err := r.app().Get(r.proxy.URL)
		Expect(err).NotTo(HaveOccurred())
		_, _ = io.ReadAll(res.Body)
		Expect(res.Trailer.Get("X-Sum")).To(Equal("7"))
		Expect(res.Trailer).NotTo(HaveKey("Content-Type"))
		Expect(res.Trailer).NotTo(HaveKey("Www-Authenticate"))
	})
})

var _ = Describe("Early replies", func() {
	It("passes a 103 back without headers for one hop only", func() {
		r := newRig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			h.Set("Link", "</s.css>; rel=preload")
			h.Set("Connection", "X-Nominated")
			h.Set("X-Nominated", "x")
			h.Set("Keep-Alive", "timeout=5")
			w.WriteHeader(http.StatusEarlyHints)
			clear(h)
			_, _ = io.WriteString(w, "final")
		}))
		var early textproto.MIMEHeader
		req, _ := http.NewRequest(http.MethodGet, r.proxy.URL, nil)
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			Got1xxResponse: func(_ int, h textproto.MIMEHeader) error { early = h; return nil },
		}))
		res, err := r.app().Do(req)
		Expect(err).NotTo(HaveOccurred())
		body, _ := io.ReadAll(res.Body)
		Expect(string(body)).To(Equal("final"))
		Expect(early.Get("Link")).NotTo(BeEmpty())
		Expect(early.Get("X-Nominated")).To(BeEmpty())
		Expect(early.Get("Keep-Alive")).To(BeEmpty())
	})
})

var _ = Describe("Upgrades", func() {
	echo := http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Header.Get("Upgrade") != "echo" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "echo")
		w.WriteHeader(http.StatusSwitchingProtocols)
		c, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, brw)
	})

	It("carries data both ways after a switch", func() {
		r := newHTTP1Rig(echo, 0)
		c, br, res := r.raw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		Expect(res.StatusCode).To(Equal(http.StatusSwitchingProtocols))
		_, _ = io.WriteString(c, "ping\n")
		Expect(br.ReadString('\n')).To(Equal("ping\n"))
	})

	It("ends the upgraded connection when the call ends", func() {
		r := newHTTP1Rig(echo, 100*time.Millisecond)
		c, br, res := r.raw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		Expect(res.StatusCode).To(Equal(http.StatusSwitchingProtocols))
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err := br.ReadByte()
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, os.ErrDeadlineExceeded)).To(BeFalse(), "the connection outlived the call")
	})

	It("refuses a switch the app did not ask for", func() {
		r := newHTTP1Rig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "echo")
			w.WriteHeader(http.StatusSwitchingProtocols)
		}), 0)
		_, _, res := r.raw("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		Expect(res.StatusCode).To(Equal(http.StatusBadGateway))
	})

	It("refuses a protocol name that is not plain text, from the app", func() {
		called := false
		r := newHTTP1Rig(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), 0)
		_, _, res := r.raw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: \xe9cho\r\n\r\n")
		Expect(res.StatusCode).To(Equal(http.StatusBadGateway))
		Expect(called).To(BeFalse())
	})

	It("refuses a protocol name that is not plain text, from the vendor", func() {
		r := newHTTP1Rig(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "Upgrade")
			w.Header()["Upgrade"] = []string{"\xe9cho"}
			w.WriteHeader(http.StatusSwitchingProtocols)
			if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = c.Close()
			}
		}), 0)
		_, _, res := r.raw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		Expect(res.StatusCode).To(Equal(http.StatusBadGateway))
	})
})

var _ = Describe("HTTP/2 on both sides", func() {
	It("forwards the body, headers and trailers", func() {
		var proto string
		r := newRig(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			proto = q.Proto
			_, _ = io.Copy(io.Discard, q.Body)
			w.Header().Set("X-Reply", "r")
			_, _ = io.WriteString(w, "ok")
			w.Header().Set(http.TrailerPrefix+"Grpc-Status", "0")
		}))
		r.proxy.Close()
		r.proxy = httptest.NewUnstartedServer(r.proxy.Config.Handler)
		r.proxy.EnableHTTP2 = true
		r.proxy.StartTLS()
		res, err := r.proxy.Client().Post(r.proxy.URL, "text/plain", strings.NewReader("hello"))
		Expect(err).NotTo(HaveOccurred())
		body, _ := io.ReadAll(res.Body)
		Expect(res.Proto).To(Equal("HTTP/2.0"))
		Expect(proto).To(Equal("HTTP/2.0"))
		Expect(string(body)).To(Equal("ok"))
		Expect(res.Header.Get("X-Reply")).To(Equal("r"))
		Expect(res.Trailer.Get("Grpc-Status")).To(Equal("0"))
	})
})
