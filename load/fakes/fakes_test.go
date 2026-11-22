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
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	logs "go.opentelemetry.io/proto/otlp/logs/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
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

	It("streams an Anthropic answer of 100 events, each sent on its own", func() {
		start("stream-sse")
		resp, err := http.Post("http://"+vendor.PlainAddr+"/v1/messages", "application/json", bytes.NewReader([]byte(`{}`)))
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(body), "\n\n")).To(Equal(100))
		Expect(string(body)).To(HavePrefix("event: message_start\n"))
		Expect(string(body)).To(HaveSuffix("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
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
		Expect(fakes.Shapes()).To(HaveKey("stream-sse"))
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

	It("counts the sends of records and of spans apart, with their bytes", func() {
		send("/v1/logs", []byte("1234567"), false)
		send("/v1/logs", []byte("123"), false)
		send("/v1/traces", []byte("12345"), false)
		send("/v1/metrics", counter("go.memory.allocated", 1), false)
		Expect(receiver.Records()).To(Equal(fakes.Sends{Requests: 2, Bytes: 10}))
		Expect(receiver.Spans()).To(Equal(fakes.Sends{Requests: 1, Bytes: 5}))
		Expect(receiver.Requests()).To(Equal(int64(4)))
	})

	It("keeps the proxy's own count of the records and spans its exporters dropped", func() {
		point := func(signal, reason string, n int64) *metrics.NumberDataPoint {
			text := func(k, v string) *common.KeyValue {
				return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
			}
			return &metrics.NumberDataPoint{Value: &metrics.NumberDataPoint_AsInt{AsInt: n},
				Attributes: []*common.KeyValue{text("hotline.exporter", "otlp"), text("hotline.signal", signal), text("hotline.drop.reason", reason)}}
		}
		dropped := func(temporality metrics.AggregationTemporality, points ...*metrics.NumberDataPoint) []byte {
			m := &metrics.Metric{Name: "hotline.exporter.dropped", Data: &metrics.Metric_Sum{Sum: &metrics.Sum{AggregationTemporality: temporality, DataPoints: points}}}
			out, err := proto.Marshal(&colmetrics.ExportMetricsServiceRequest{
				ResourceMetrics: []*metrics.ResourceMetrics{{ScopeMetrics: []*metrics.ScopeMetrics{{Metrics: []*metrics.Metric{m}}}}},
			})
			Expect(err).NotTo(HaveOccurred())
			return out
		}
		total := metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
		send("/telemetry/v1/metrics", dropped(total, point("record", "queue_full", 5), point("span", "send_failed", 2)), false)
		// A total since the start replaces the last one.
		send("/telemetry/v1/metrics", dropped(total, point("record", "queue_full", 7)), false)
		// Growth since the last send is added.
		send("/telemetry/v1/metrics", dropped(metrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, point("record", "send_failed", 3)), false)
		records, spans := receiver.Dropped()
		Expect(records).To(Equal(int64(10)))
		Expect(spans).To(Equal(int64(2)))
	})

	It("counts the WebSocket records by what their check found, and every span", func() {
		text := func(k, v string) *common.KeyValue {
			return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
		}
		message := func(attrs ...*common.KeyValue) *logs.LogRecord {
			return &logs.LogRecord{EventName: "hotline.websocket.message", Attributes: attrs}
		}
		records := []*logs.LogRecord{
			message(text("hotline.websocket.message.name", "serverContent"), text("hotline.schema.status", "valid")),
			message(text("hotline.websocket.message.name", "toolCall"), text("hotline.schema.status", "invalid")),
			message(text("hotline.websocket.message.name", "serverContent"), text("hotline.schema.skipped", "overloaded"), text("hotline.schema.status", "skipped")),
			message(text("hotline.websocket.message.name", "unknown"), text("hotline.schema.skipped", "overloaded")),
			// An older proxy names the event in an attribute.
			{Attributes: []*common.KeyValue{text("event.name", "hotline.websocket.message"), text("hotline.websocket.message.name", "unknown")}},
			{EventName: "hotline.websocket.session", Attributes: []*common.KeyValue{{Key: "hotline.websocket.unread_from_index", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 4}}}}},
			{EventName: "hotline.websocket.session"},
			{EventName: "hotline.request"},
		}
		body, err := proto.Marshal(&collogs.ExportLogsServiceRequest{ResourceLogs: []*logs.ResourceLogs{{ScopeLogs: []*logs.ScopeLogs{{LogRecords: records}}}}})
		Expect(err).NotTo(HaveOccurred())
		send("/v1/logs", body, true)
		spans, err := proto.Marshal(&coltrace.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{ScopeSpans: []*trace.ScopeSpans{{Spans: []*trace.Span{{Name: "websocket message"}, {Name: "message spec check"}, {Name: "websocket session"}}}}}}})
		Expect(err).NotTo(HaveOccurred())
		send("/v1/traces", spans, false)

		m := receiver.Messages()
		Expect(m.Records).To(Equal(int64(5)))
		Expect(m.Valid).To(Equal(int64(1)))
		Expect(m.Invalid).To(Equal(int64(1)))
		Expect(m.Unknown).To(Equal(int64(2)))
		Expect(m.Skipped).To(Equal(map[string]int64{"overloaded": 2}))
		Expect(m.Sessions).To(Equal(int64(2)))
		Expect(m.Unread).To(Equal(int64(1)))
		Expect(m.Spans).To(Equal(int64(3)))
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
