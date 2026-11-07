package fakes_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

	"hotline-load/fakes"
)

var _ = Describe("The fake vendor", func() {
	var vendor *fakes.Vendor

	start := func(shape string) {
		s, ok := fakes.Shapes()[shape]
		Expect(ok).To(BeTrue(), "no call shape called "+shape)
		var err error
		vendor, err = fakes.StartVendor(s)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(vendor.Close)
	}

	post := func(client *http.Client, url string) (*http.Response, []byte) {
		resp, err := client.Post(url, "application/x-amz-json-1.1", bytes.NewReader([]byte(`{"Name":"x"}`)))
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp, body
	}

	trusting := func() *http.Client {
		pool := x509.NewCertPool()
		Expect(pool.AppendCertsFromPEM(vendor.CertPEM)).To(BeTrue())
		return &http.Client{Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		}}
	}

	It("answers a small call with a small JSON body over TLS and HTTP/2", func() {
		start("small-json")
		resp, body := post(trusting(), "https://"+vendor.TLSAddr+"/")
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.ProtoMajor).To(Equal(2))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/x-amz-json-1.1"))
		Expect(len(body)).To(BeNumerically("<", 1024))
	})

	It("answers the same over plain HTTP, for the call without the proxy", func() {
		start("small-json")
		resp, body := post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(resp.StatusCode).To(Equal(200))
		Expect(len(body)).To(BeNumerically("<", 1024))
	})

	It("answers a large call with about 115 KB of JSON", func() {
		start("large-json")
		_, body := post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(len(body)).To(BeNumerically("~", 115*1024, 15*1024))
	})

	It("answers a medium call with about 30 KB of JSON", func() {
		start("medium-json")
		_, body := post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(len(body)).To(BeNumerically("~", 30*1024, 5*1024))
	})

	It("answers a huge call with about 500 KB of JSON", func() {
		start("huge-json")
		_, body := post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(len(body)).To(BeNumerically("~", 500*1024, 50*1024))
	})

	It("answers an XML call with XML", func() {
		start("xml")
		resp, body := post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(string(body)).To(HavePrefix("<?xml"))
	})

	It("makes a slow vendor wait before it answers", func() {
		start("slow")
		began := time.Now()
		post(http.DefaultClient, "http://"+vendor.PlainAddr+"/")
		Expect(time.Since(began)).To(BeNumerically(">=", 50*time.Millisecond))
	})

	It("keeps nothing of what it is sent", func() {
		start("small-json")
		big := bytes.Repeat([]byte("x"), 1<<20)
		resp, err := http.Post("http://"+vendor.PlainAddr+"/", "text/plain", bytes.NewReader(big))
		Expect(err).NotTo(HaveOccurred())
		_ = resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
	})

	It("names the call shapes it knows", func() {
		Expect(fakes.Shapes()).To(HaveKey("small-json"))
		Expect(fakes.Shapes()).To(HaveKey("large-json"))
		Expect(fakes.Shapes()).To(HaveKey("medium-json"))
		Expect(fakes.Shapes()).To(HaveKey("huge-json"))
		Expect(fakes.Shapes()).To(HaveKey("xml"))
		Expect(fakes.Shapes()).To(HaveKey("slow"))
	})
})

var _ = Describe("The fake receiver", func() {
	var receiver *fakes.Receiver

	BeforeEach(func() {
		var err error
		receiver, err = fakes.StartReceiver()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(receiver.Close)
	})

	send := func(path string, body []byte, gzipped bool) *http.Response {
		if gzipped {
			var b bytes.Buffer
			w := gzip.NewWriter(&b)
			_, _ = w.Write(body)
			Expect(w.Close()).To(Succeed())
			body = b.Bytes()
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+receiver.Addr+path, bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/x-protobuf")
		if gzipped {
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		_ = resp.Body.Close()
		return resp
	}

	counterWith := func(name string, value int64, temporality metrics.AggregationTemporality) []byte {
		point := &metrics.NumberDataPoint{Value: &metrics.NumberDataPoint_AsInt{AsInt: value}}
		m := &metrics.Metric{Name: name, Data: &metrics.Metric_Sum{Sum: &metrics.Sum{
			AggregationTemporality: temporality, DataPoints: []*metrics.NumberDataPoint{point}}}}
		out, err := proto.Marshal(&colmetrics.ExportMetricsServiceRequest{
			ResourceMetrics: []*metrics.ResourceMetrics{{ScopeMetrics: []*metrics.ScopeMetrics{{Metrics: []*metrics.Metric{m}}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		return out
	}

	counter := func(name string, value int64) []byte {
		return counterWith(name, value, metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE)
	}

	It("adds up what a proxy sends as the growth since its last send", func() {
		delta := metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
		send("/v1/metrics", counterWith("go.memory.allocated", 100, delta), false)
		send("/v1/metrics", counterWith("go.memory.allocated", 250, delta), false)
		allocated, ok := receiver.Allocated()
		Expect(ok).To(BeTrue())
		Expect(allocated).To(Equal(int64(350)))
	})

	It("counts the requests and bytes it is sent, and answers 200", func() {
		resp := send("/v1/traces", []byte("12345"), false)
		Expect(resp.StatusCode).To(Equal(200))
		send("/v1/logs", []byte("1234567"), false)
		Expect(receiver.Requests()).To(Equal(int64(2)))
		Expect(receiver.Bytes()).To(Equal(int64(12)))
	})

	It("reads the proxy's memory handed out from a gzipped send of its own numbers", func() {
		send("/v1/metrics", counter("go.memory.allocated", 4096), true)
		allocated, ok := receiver.Allocated()
		Expect(ok).To(BeTrue())
		Expect(allocated).To(Equal(int64(4096)))
	})

	It("keeps the latest value when the proxy sends the total since its start", func() {
		send("/v1/metrics", counter("go.memory.allocated", 100), false)
		send("/v1/metrics", counter("go.memory.allocated", 300), false)
		allocated, _ := receiver.Allocated()
		Expect(allocated).To(Equal(int64(300)))
	})

	It("says so when no number of memory handed out has arrived", func() {
		send("/v1/metrics", counter("go.goroutine.count", 7), false)
		_, ok := receiver.Allocated()
		Expect(ok).To(BeFalse())
	})

	It("refuses a body it cannot read as OTLP metrics with 400", func() {
		resp := send("/v1/metrics", []byte("not protobuf at all \xff\xff"), false)
		Expect(resp.StatusCode).To(Equal(400))
	})
})

var _ = Describe("The fake receiver forwarding the proxy's own numbers", func() {
	type got struct {
		path, user, pass, contentType string
		body                          []byte
	}
	var (
		mu       sync.Mutex
		arrived  []got
		upstream *httptest.Server
		status   int
	)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(arrived)
	}

	BeforeEach(func() {
		arrived, status = nil, http.StatusOK
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			user, pass, _ := r.BasicAuth()
			mu.Lock()
			arrived = append(arrived, got{r.URL.Path, user, pass, r.Header.Get("Content-Type"), body})
			code := status
			mu.Unlock()
			w.WriteHeader(code)
		}))
		DeferCleanup(upstream.Close)
	})

	start := func(f *fakes.Forward) *fakes.Receiver {
		r, err := fakes.StartReceiver()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(r.Close)
		r.Forward(f)
		return r
	}
	metricsBody := func() []byte {
		m := &metrics.Metric{Name: "go.goroutine.count", Data: &metrics.Metric_Gauge{Gauge: &metrics.Gauge{
			DataPoints: []*metrics.NumberDataPoint{{Value: &metrics.NumberDataPoint_AsInt{AsInt: 7}}}}}}
		out, err := proto.Marshal(&colmetrics.ExportMetricsServiceRequest{
			ResourceMetrics: []*metrics.ResourceMetrics{{ScopeMetrics: []*metrics.ScopeMetrics{{Metrics: []*metrics.Metric{m}}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		return out
	}
	post := func(r *fakes.Receiver, path string, body []byte) {
		resp, err := http.Post("http://"+r.Addr+path, "application/x-protobuf", bytes.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		_ = resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
	}

	It("passes the telemetry destination's metrics on, signed in with the instance id and token", func() {
		r := start(&fakes.Forward{URL: upstream.URL + "/otlp", Username: "42", Password: "token"})
		body := metricsBody()
		post(r, fakes.TelemetryPath+"/v1/metrics", body)
		Eventually(count).Should(Equal(1))
		mu.Lock()
		defer mu.Unlock()
		Expect(arrived[0].path).To(Equal("/otlp/v1/metrics"))
		Expect(arrived[0].user).To(Equal("42"))
		Expect(arrived[0].pass).To(Equal("token"))
		Expect(arrived[0].contentType).To(Equal("application/x-protobuf"))
		Expect(arrived[0].body).To(Equal(body))
	})

	It("does not pass on the exporters' counts, records or spans", func() {
		r := start(&fakes.Forward{URL: upstream.URL + "/otlp", Username: "42", Password: "token"})
		post(r, "/v1/metrics", metricsBody())
		post(r, "/v1/logs", []byte("records"))
		post(r, fakes.TelemetryPath+"/v1/traces", []byte("spans"))
		Consistently(count, "300ms").Should(Equal(0))
	})

	It("still reads the proxy's memory from what it forwards", func() {
		r := start(&fakes.Forward{URL: upstream.URL + "/otlp", Username: "42", Password: "token"})
		point := &metrics.NumberDataPoint{Value: &metrics.NumberDataPoint_AsInt{AsInt: 4096}}
		m := &metrics.Metric{Name: "go.memory.allocated", Data: &metrics.Metric_Sum{Sum: &metrics.Sum{DataPoints: []*metrics.NumberDataPoint{point}}}}
		out, err := proto.Marshal(&colmetrics.ExportMetricsServiceRequest{
			ResourceMetrics: []*metrics.ResourceMetrics{{ScopeMetrics: []*metrics.ScopeMetrics{{Metrics: []*metrics.Metric{m}}}}},
		})
		Expect(err).NotTo(HaveOccurred())
		post(r, fakes.TelemetryPath+"/v1/metrics", out)
		allocated, ok := r.Allocated()
		Expect(ok).To(BeTrue())
		Expect(allocated).To(Equal(int64(4096)))
	})

	It("forwards nothing when it is not told where to", func() {
		r := start(nil)
		post(r, fakes.TelemetryPath+"/v1/metrics", metricsBody())
		Consistently(count, "300ms").Should(Equal(0))
	})

	It("counts the sends Grafana refused, and never lets that fail the answer to the proxy", func() {
		mu.Lock()
		status = http.StatusUnauthorized
		mu.Unlock()
		r := start(&fakes.Forward{URL: upstream.URL + "/otlp", Username: "42", Password: "bad"})
		post(r, fakes.TelemetryPath+"/v1/metrics", metricsBody())
		Eventually(r.ForwardFailures).Should(Equal(int64(1)))
		Expect(r.ForwardError()).To(ContainSubstring("401"))
	})
})
