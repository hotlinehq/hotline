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

var _ = Describe("Forwarding a call", func() {
	sut := forwardingSUT{}

	It("leaves the app's own headers whole", func() {
		sut.forVendor(quietVendor())
		sut.Call(sut.Request(http.MethodGet, nil, http.Header{
			"Connection": {"X-Drop"}, "X-Drop": {"1"}, "Te": {"trailers"}, "X-Keep": {"a", "b"},
		}))
		before, after := sut.AppHeadersBeforeAndAfterForwarding()
		Expect(after).To(Equal(before))
		Expect(after.Get("X-Drop")).To(Equal("1"))
	})

	It("sends what is for one hop only no further, either way", func() {
		sut.forHTTP1Vendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "X-Secret")
			w.Header().Set("X-Secret", "hidden")
			w.Header().Add("X-Multi", "a")
			w.Header().Add("X-Multi", "b")
		}))
		res := sut.Call(sut.Request(http.MethodGet, nil, http.Header{
			"Connection": {"X-Drop"}, "X-Drop": {"x"}, "Proxy-Authorization": {"Basic abc"},
			"X-Forwarded-For": {"10.0.0.1"}, "Te": {"trailers"},
		}))
		got := sut.VendorGotHeader()
		Expect(got).NotTo(HaveKey("X-Drop"))
		Expect(got).NotTo(HaveKey("Proxy-Authorization"))
		Expect(got).NotTo(HaveKey("X-Forwarded-For"))
		Expect(got.Get("Te")).To(Equal("trailers"))
		Expect(res.Header).NotTo(HaveKey("X-Secret"))
		Expect(res.Header["X-Multi"]).To(Equal([]string{"a", "b"}))
	})

	It("adds no User-Agent of its own", func() {
		sut.forVendor(quietVendor())
		sut.Call(sut.Request(http.MethodGet, nil, http.Header{"User-Agent": nil}))
		Expect(sut.VendorGotCall()).To(BeTrue())
		Expect(sut.VendorGotHeader()["User-Agent"]).To(BeEmpty())
	})

	DescribeTable("passes bodies on whole",
		func(body io.Reader, length int64, want int) {
			sut.forVendorReadingTheBody()
			sut.SendBody(body, length)
			Expect(sut.VendorGotBody()).To(HaveLen(want))
		},
		Entry("with a length", strings.NewReader("hello"), int64(5), 5),
		Entry("empty", nil, int64(0), 0),
		Entry("in chunks", io.MultiReader(strings.NewReader("ab"), strings.NewReader("cd")), int64(-1), 4),
		Entry("of a megabyte", strings.NewReader(strings.Repeat("x", 1<<20)), int64(1<<20), 1<<20),
	)

	It("stops reading the app's body before it returns, when the vendor answers early", func() {
		sut.forVendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}))
		Expect(sut.StatusesOfLargeUploadsWhileTheyAreWritten(10)).To(HaveEach(http.StatusRequestEntityTooLarge))
	})

	It("passes a streamed response on part by part", func() {
		sut.forVendorSendingOneEventThenWaiting()
		Expect(sut.FirstLineOfTheResponse()).To(Equal("data: 1\n"))
	})

	It("breaks the app's response when the vendor breaks off mid response", func() {
		sut.forVendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "part")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}))
		Expect(sut.ReadBodyError(sut.Call(sut.Request(http.MethodGet, nil, nil)))).To(HaveOccurred())
	})

	It("returns an error when no response comes, for the caller to answer", func() {
		sut.forVendor(quietVendor())
		sut.StopTheVendor()
		Expect(sut.Call(sut.Request(http.MethodGet, nil, nil)).StatusCode).To(Equal(http.StatusBadGateway))
	})
})

var _ = Describe("Trailers", func() {
	sut := forwardingSUT{}

	It("passes the values of the app's trailers on", func() {
		sut.forVendorReadingTheBody()
		sut.UploadWithTrailer("upload", "X-Checksum", "crc32=3610a686")
		Expect(sut.VendorGotTrailer().Get("X-Checksum")).To(Equal("crc32=3610a686"))
	})

	DescribeTable("passes the vendor's trailers back, announced or not",
		func(announce bool) {
			sut.forVendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if announce {
					w.Header().Set("Trailer", "Grpc-Status")
				}
				_, _ = io.WriteString(w, "messages")
				w.(http.Flusher).Flush()
				w.Header().Set(http.TrailerPrefix+"Grpc-Status", "5")
			}))
			res := sut.CallAndReadBody(sut.Request(http.MethodGet, nil, nil))
			Expect(res.Trailer.Get("Grpc-Status")).To(Equal("5"))
		},
		Entry("announced", true),
		Entry("not announced", false),
	)

	It("drops fields that must not come after the body", func() {
		// Over HTTP/1.1: Go's HTTP/2 server will not send such trailers.
		sut.forHTTP1Vendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "body")
			w.(http.Flusher).Flush()
			w.Header().Set(http.TrailerPrefix+"X-Sum", "7")
			w.Header().Set(http.TrailerPrefix+"Content-Type", "text/evil")
			w.Header().Set(http.TrailerPrefix+"Www-Authenticate", "Basic")
		}))
		res := sut.CallAndReadBody(sut.Request(http.MethodGet, nil, nil))
		Expect(res.Trailer.Get("X-Sum")).To(Equal("7"))
		Expect(res.Trailer).NotTo(HaveKey("Content-Type"))
		Expect(res.Trailer).NotTo(HaveKey("Www-Authenticate"))
	})
})

var _ = Describe("Early responses", func() {
	sut := forwardingSUT{}

	It("passes a 103 back without headers for one hop only", func() {
		sut.forVendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			h := w.Header()
			h.Set("Link", "</s.css>; rel=preload")
			h.Set("Connection", "X-Nominated")
			h.Set("X-Nominated", "x")
			h.Set("Keep-Alive", "timeout=5")
			w.WriteHeader(http.StatusEarlyHints)
			clear(h)
			_, _ = io.WriteString(w, "final")
		}))
		early, body := sut.CallWatchingEarlyResponses()
		Expect(body).To(Equal("final"))
		Expect(early.Get("Link")).NotTo(BeEmpty())
		Expect(early.Get("X-Nominated")).To(BeEmpty())
		Expect(early.Get("Keep-Alive")).To(BeEmpty())
	})
})

var _ = Describe("Upgrades", func() {
	sut := forwardingSUT{}

	It("carries data both ways after a switch", func() {
		sut.forHTTP1Vendor(echoVendor())
		Expect(sut.SendRaw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")).To(Equal(http.StatusSwitchingProtocols))
		Expect(sut.SendAfterTheSwitch("ping\n")).To(Equal("ping\n"))
	})

	It("ends the upgraded connection when the call ends", func() {
		sut.forHTTP1VendorWithCallsLasting(echoVendor(), 100*time.Millisecond)
		Expect(sut.SendRaw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")).To(Equal(http.StatusSwitchingProtocols))
		err := sut.ReadAfterTheSwitchWithin(3 * time.Second)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, os.ErrDeadlineExceeded)).To(BeFalse(), "the connection outlived the call")
	})

	It("refuses a switch the app did not ask for", func() {
		sut.forHTTP1Vendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", "echo")
			w.WriteHeader(http.StatusSwitchingProtocols)
		}))
		Expect(sut.SendRaw("GET / HTTP/1.1\r\nHost: x\r\n\r\n")).To(Equal(http.StatusBadGateway))
	})

	It("refuses a protocol name that is not plain text, from the app", func() {
		sut.forHTTP1Vendor(quietVendor())
		Expect(sut.SendRaw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: \xe9cho\r\n\r\n")).To(Equal(http.StatusBadGateway))
		Expect(sut.VendorGotCall()).To(BeFalse())
	})

	It("refuses a protocol name that is not plain text, from the vendor", func() {
		sut.forHTTP1Vendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "Upgrade")
			w.Header()["Upgrade"] = []string{"\xe9cho"}
			w.WriteHeader(http.StatusSwitchingProtocols)
			if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = c.Close()
			}
		}))
		Expect(sut.SendRaw("GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")).To(Equal(http.StatusBadGateway))
	})
})

var _ = Describe("HTTP/2 on both sides", func() {
	sut := forwardingSUT{}

	It("forwards the body, headers and trailers", func() {
		sut.forHTTP2App(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			_, _ = io.Copy(io.Discard, q.Body)
			w.Header().Set("X-Response", "r")
			_, _ = io.WriteString(w, "ok")
			w.Header().Set(http.TrailerPrefix+"Grpc-Status", "0")
		}))
		res, body := sut.PostOverHTTP2("hello")
		Expect(res.Proto).To(Equal("HTTP/2.0"))
		Expect(sut.VendorGotProto()).To(Equal("HTTP/2.0"))
		Expect(body).To(Equal("ok"))
		Expect(res.Header.Get("X-Response")).To(Equal("r"))
		Expect(res.Trailer.Get("Grpc-Status")).To(Equal("0"))
	})
})

// forwardingSUT is an app's proxy with a forwarder in front of one fake
// vendor, and what the vendor and the forwarder saw of each call.
type forwardingSUT struct {
	vendor, proxy *httptest.Server
	// calls is how long each call's context lasts.
	calls time.Duration
	// readBody makes the vendor read each call's body to its end, trailers
	// and all, before its handler runs.
	readBody bool
	// conn and reader are a raw connection to the proxy port, for upgrades.
	conn   net.Conn
	reader *bufio.Reader

	mu sync.Mutex
	// before and after are the app's request headers when the call came in
	// and once it was forwarded.
	before, after http.Header
	// seen is what the vendor got of the last call.
	seen    bool
	header  http.Header
	body    []byte
	trailer http.Header
	proto   string
}

// forVendor is a forwarder speaking HTTP/2 over TLS to vendor, behind a
// proxy the app reaches over plain HTTP. Each call's context lasts a
// minute.
func (s *forwardingSUT) forVendor(vendor http.Handler) {
	s.reset(time.Minute)
	s.vendor = httptest.NewUnstartedServer(s.recording(vendor))
	s.vendor.EnableHTTP2 = true
	s.vendor.StartTLS()
	u, err := url.Parse(s.vendor.URL)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	transport := s.vendor.Client().Transport.(*http.Transport).Clone()
	transport.DisableCompression = true
	s.proxy = httptest.NewUnstartedServer(s.forwarding(true, sender(func(out *http.Request) (*http.Response, error) {
		target := *u
		target.Path, target.RawPath, target.RawQuery = out.URL.Path, out.URL.RawPath, out.URL.RawQuery
		out.URL = &target
		out.Host = "api.vendor.test"
		out.Header.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
		return transport.RoundTrip(out)
	})))
	s.proxy.EnableHTTP2 = true
	s.proxy.Start()
	DeferCleanup(s.Close)
}

// forVendorSendingOneEventThenWaiting is a forwarder in front of a vendor
// that sends one event, flushes it, and sends nothing more until the test
// ends.
func (s *forwardingSUT) forVendorSendingOneEventThenWaiting() {
	release := make(chan struct{})
	s.forVendor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	// Cleanups run last first, so the vendor is let go before the sut
	// stops it.
	DeferCleanup(func() { close(release) })
}

// forVendorReadingTheBody is a forwarder in front of a vendor that reads
// each call's body to its end and answers nothing more.
func (s *forwardingSUT) forVendorReadingTheBody() {
	s.forVendor(quietVendor())
	s.readBody = true
}

// forHTTP2App is a forwarder whose app reaches the proxy over HTTP/2 with
// TLS too.
func (s *forwardingSUT) forHTTP2App(vendor http.Handler) {
	s.forVendor(vendor)
	handler := s.proxy.Config.Handler
	s.proxy.Close()
	s.proxy = httptest.NewUnstartedServer(handler)
	s.proxy.EnableHTTP2 = true
	s.proxy.StartTLS()
}

// forHTTP1Vendor is a forwarder whose vendor and app speak HTTP/1.1 only,
// as an upgrade needs.
func (s *forwardingSUT) forHTTP1Vendor(vendor http.Handler) {
	s.forHTTP1VendorWithCallsLasting(vendor, time.Minute)
}

// forHTTP1VendorWithCallsLasting is an HTTP/1.1 forwarder whose calls end
// after calls.
func (s *forwardingSUT) forHTTP1VendorWithCallsLasting(vendor http.Handler, calls time.Duration) {
	s.reset(calls)
	s.vendor = httptest.NewServer(s.recording(vendor))
	u, err := url.Parse(s.vendor.URL)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	transport := &http.Transport{}
	s.proxy = httptest.NewServer(s.forwarding(false, sender(func(out *http.Request) (*http.Response, error) {
		out.URL.Scheme, out.URL.Host = u.Scheme, u.Host
		return transport.RoundTrip(out)
	})))
	DeferCleanup(s.Close)
}

// Close stops the proxy and the vendor.
func (s *forwardingSUT) Close() {
	s.proxy.Close()
	s.vendor.Close()
}

// StopTheVendor stops the vendor, so no response comes.
func (s *forwardingSUT) StopTheVendor() { s.vendor.Close() }

// Request is a call to the proxy, with these headers.
func (s *forwardingSUT) Request(method string, body io.Reader, header http.Header) *http.Request {
	req, err := http.NewRequestWithContext(context.Background(), method, s.proxy.URL, body)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	for name, values := range header {
		req.Header[name] = values
	}
	return req
}

// Call sends the app's call and gives the response; its body is closed
// once the test ends.
func (s *forwardingSUT) Call(req *http.Request) *http.Response {
	res, err := s.app().Do(req)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(res.Body.Close)
	return res
}

// CallAndReadBody sends the app's call and reads the response's body to
// its end, so its trailers are there.
func (s *forwardingSUT) CallAndReadBody(req *http.Request) *http.Response {
	res := s.Call(req)
	_, _ = io.ReadAll(res.Body)
	return res
}

// ReadBodyError is why reading a response's body failed.
func (*forwardingSUT) ReadBodyError(res *http.Response) error {
	_, err := io.ReadAll(res.Body)
	return err
}

// SendBody posts body, announced as length bytes, -1 for unknown.
func (s *forwardingSUT) SendBody(body io.Reader, length int64) {
	req := s.Request(http.MethodPost, body, nil)
	req.ContentLength = length
	res := s.Call(req)
	_, _ = io.ReadAll(res.Body)
	Expect(res.Body.Close()).WithOffset(1).To(Succeed())
}

// UploadWithTrailer puts body, then sends a trailer once the body is
// written.
func (s *forwardingSUT) UploadWithTrailer(body, name, value string) {
	pr, pw := io.Pipe()
	req := s.Request(http.MethodPut, pr, nil)
	req.Trailer = http.Header{name: nil}
	go func() {
		_, _ = io.WriteString(pw, body)
		req.Trailer.Set(name, value)
		_ = pw.Close()
	}()
	res := s.Call(req)
	_, _ = io.ReadAll(res.Body)
	Expect(res.Body.Close()).WithOffset(1).To(Succeed())
}

// StatusesOfLargeUploadsWhileTheyAreWritten posts n bodies of a megabyte,
// each written while the call is under way, and gives the statuses of the
// calls that got an answer.
func (s *forwardingSUT) StatusesOfLargeUploadsWhileTheyAreWritten(n int) []int {
	var statuses []int
	for range n {
		pr, pw := io.Pipe()
		go func() {
			for range 64 {
				if _, err := pw.Write(make([]byte, 16<<10)); err != nil {
					return
				}
			}
			_ = pw.Close()
		}()
		req := s.Request(http.MethodPost, pr, nil)
		req.ContentLength = 1 << 20
		if res, err := s.app().Do(req); err == nil {
			statuses = append(statuses, res.StatusCode)
			_ = res.Body.Close()
		}
		_ = pr.Close()
	}
	return statuses
}

// FirstLineOfTheResponse gets the proxy and reads the first line of the
// response's body.
func (s *forwardingSUT) FirstLineOfTheResponse() string {
	res := s.Call(s.Request(http.MethodGet, nil, nil))
	defer func() { _ = res.Body.Close() }()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return line
}

// CallWatchingEarlyResponses gets the proxy, and gives the headers of the
// early response that came and the final body.
func (s *forwardingSUT) CallWatchingEarlyResponses() (textproto.MIMEHeader, string) {
	var early textproto.MIMEHeader
	req := s.Request(http.MethodGet, nil, nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		Got1xxResponse: func(_ int, h textproto.MIMEHeader) error { early = h; return nil },
	}))
	res := s.Call(req)
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return early, string(body)
}

// PostOverHTTP2 posts body to the proxy over HTTP/2, and gives the
// response and its body.
func (s *forwardingSUT) PostOverHTTP2(body string) (*http.Response, string) {
	req := s.Request(http.MethodPost, strings.NewReader(body), http.Header{"Content-Type": {"text/plain"}})
	res, err := s.proxy.Client().Do(req)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(res.Body.Close)
	got, err := io.ReadAll(res.Body)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return res, string(got)
}

// SendRaw sends text straight to the proxy port, and gives the status of
// the first answer. The connection stays open for the test.
func (s *forwardingSUT) SendRaw(text string) int {
	c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", strings.TrimPrefix(s.proxy.URL, "http://"))
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	DeferCleanup(c.Close)
	Expect(c.SetDeadline(time.Now().Add(5 * time.Second))).WithOffset(1).To(Succeed())
	_, err = io.WriteString(c, text)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.conn, s.reader = c, bufio.NewReader(c)
	res, err := http.ReadResponse(s.reader, nil)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	// An answer that switches the protocol has no body; the bytes after it
	// stay in s.reader.
	Expect(res.Body.Close()).WithOffset(1).To(Succeed())
	return res.StatusCode
}

// SendAfterTheSwitch writes a line on the switched connection, and reads
// the line that comes back.
func (s *forwardingSUT) SendAfterTheSwitch(line string) string {
	_, err := io.WriteString(s.conn, line)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	got, err := s.reader.ReadString('\n')
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return got
}

// ReadAfterTheSwitchWithin reads one byte on the switched connection,
// waiting at most limit, and says why it could not.
func (s *forwardingSUT) ReadAfterTheSwitchWithin(limit time.Duration) error {
	Expect(s.conn.SetReadDeadline(time.Now().Add(limit))).WithOffset(1).To(Succeed())
	_, err := s.reader.ReadByte()
	return err
}

// AppHeadersBeforeAndAfterForwarding are the app's request headers when
// the call came in and once it was forwarded, waiting for the forwarder
// to be done.
func (s *forwardingSUT) AppHeadersBeforeAndAfterForwarding() (http.Header, http.Header) {
	Eventually(func() http.Header {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.after
	}).WithOffset(1).ShouldNot(BeNil())
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.before, s.after
}

// VendorGotCall says the vendor got a call.
func (s *forwardingSUT) VendorGotCall() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// VendorGotHeader is the header of the last call the vendor got.
func (s *forwardingSUT) VendorGotHeader() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header
}

// VendorGotBody is the body of the last call the vendor got.
func (s *forwardingSUT) VendorGotBody() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body
}

// VendorGotTrailer is the trailer of the last call the vendor got.
func (s *forwardingSUT) VendorGotTrailer() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trailer
}

// VendorGotProto is the HTTP version of the last call the vendor got.
func (s *forwardingSUT) VendorGotProto() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proto
}

func (s *forwardingSUT) reset(calls time.Duration) {
	s.calls, s.readBody, s.conn, s.reader = calls, false, nil, nil
	s.before, s.after, s.seen, s.header, s.body, s.trailer, s.proto = nil, nil, false, nil, nil, nil, ""
}

// recording keeps what the vendor got of each call before vendor answers.
func (s *forwardingSUT) recording(vendor http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		var body []byte
		if s.readBody {
			body, _ = io.ReadAll(q.Body)
		}
		s.mu.Lock()
		s.seen, s.header, s.body, s.trailer, s.proto = true, q.Header.Clone(), body, q.Trailer.Clone(), q.Proto
		s.mu.Unlock()
		vendor.ServeHTTP(w, q)
	})
}

// forwarding is the proxy's handler: it forwards each call with vendorCall,
// answering 502 when no response came, and keeps the app's headers when
// keepHeaders is set.
func (s *forwardingSUT) forwarding(keepHeaders bool, vendorCall http.RoundTripper) http.Handler {
	f := &forward.Forwarder{Buffers: &buffers{}}
	return http.HandlerFunc(func(w http.ResponseWriter, in *http.Request) {
		before := in.Header.Clone()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(in.Context()), s.calls)
		defer cancel()
		if err := f.Forward(ctx, w, in, vendorCall); err != nil {
			w.WriteHeader(http.StatusBadGateway)
		}
		if keepHeaders {
			s.mu.Lock()
			s.before, s.after = before, in.Header.Clone()
			s.mu.Unlock()
		}
	})
}

func (*forwardingSUT) app() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableCompression: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// quietVendor answers 200 with nothing.
func quietVendor() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) }

// echoVendor switches to the echo protocol when asked, and sends back
// every byte it gets.
func echoVendor() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
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
}

// sender is a vendor call made by a function.
type sender func(out *http.Request) (*http.Response, error)

func (s sender) RoundTrip(out *http.Request) (*http.Response, error) { return s(out) }
