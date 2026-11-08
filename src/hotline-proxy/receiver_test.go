package proxy_test

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"

	// gzip lets the gRPC side take gzipped exports.
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// receivedRecord is one log record the fake OTLP receiver got.
type receivedRecord struct {
	eventName string
	// timestamp is the record's time, in nanoseconds since 1970.
	timestamp uint64
	// severityText and severityNumber are the record's level.
	severityText   string
	severityNumber int32
	resource       map[string]any
	attrs          map[string]any
	// traceID, spanID and flags are the record's own trace fields, the ids
	// in hex.
	traceID string
	spanID  string
	flags   uint32
}

// receivedSpan is one span the fake OTLP receiver got, the ids in hex.
type receivedSpan struct {
	traceID string
	spanID  string
	// parent is empty for a span with no parent.
	parent string
	name   string
	// kind is server, client or internal.
	kind string
	// start and end are in nanoseconds since 1970.
	start int64
	end   int64
	// status is unset, ok or error.
	status string
	// flags are the span's OTLP flags.
	flags    uint32
	attrs    map[string]any
	resource map[string]any
}

// receivedCount is one data point of the hotline.requests counter.
type receivedCount struct {
	unit        string
	monotonic   bool
	temporality metrics.AggregationTemporality
	resource    map[string]any
	attrs       map[string]any
	value       int64
}

// receivedPoint is one data point of any metric the receiver got.
type receivedPoint struct {
	name string
	unit string
	// description is the metric's help text.
	description string
	// kind is sum, gauge or histogram.
	kind  string
	attrs map[string]any
	// value is a sum's or gauge's value, or a histogram's total.
	value  float64
	count  uint64
	bounds []float64
}

// seenRequest is one export request the receiver got, of either kind.
type seenRequest struct {
	logs        bool
	path        string
	contentType string
	// encoding is gzip or empty.
	encoding      string
	authorization []string
	// header holds every header of an HTTP request.
	header     http.Header
	tls        bool
	clientCert bool
	// proto is the HTTP version an HTTP request came with, such as
	// HTTP/1.1 or HTTP/2.0.
	proto string
}

// answer is what the HTTP side answers a log export with, instead of
// taking it.
type answer struct {
	status     int
	retryAfter string
}

// receiverSetup says how a fake receiver listens.
type receiverSetup struct {
	http bool
	// path goes in front of /v1/logs and /v1/metrics.
	path string
	// tls, when set, makes the receiver accept only TLS, with this setup.
	tls *tls.Config
	// listener, when set, is where the receiver listens.
	listener net.Listener
	// http2 offers HTTP/2 over TLS; h2c takes HTTP/2 on a plain port.
	http2 bool
	h2c   bool
}

// receiver is a fake OTLP destination on a local port. It has a gRPC side
// or an HTTP side.
type receiver struct {
	collogs.UnimplementedLogsServiceServer

	server     *grpc.Server
	httpServer *http.Server
	http       bool
	// endpoint is how the config names the receiver.
	endpoint string

	mu          sync.Mutex
	records     []receivedRecord
	counts      []receivedCount
	points      []receivedPoint
	histograms  int
	otherMetric []string
	spanList    []receivedSpan
	// logsHeld counts log exports that are waiting on hold.
	logsHeld int
	// hold, while open, keeps every export waiting.
	hold chan struct{}
	// delay slows every export down.
	delay time.Duration
	// seen are the export requests, in the order they came.
	seen []seenRequest
	// answers are given to the first log exports over HTTP, one each,
	// and grpcAnswers to the first ones over gRPC.
	answers     []answer
	grpcAnswers []codes.Code
	// encodings are the compression of each gRPC export, as the gRPC
	// side saw it.
	encodings []string
}

func newReceiver() *receiver {
	return newReceiverWith(receiverSetup{})
}

func newReceiverWith(setup receiverSetup) *receiver {
	listener := setup.listener
	if listener == nil {
		listener = listenLocal()
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	r := &receiver{http: setup.http, endpoint: listener.Addr().String()}
	if setup.tls != nil {
		r.endpoint = "https://localhost:" + port
	}
	if setup.http {
		if setup.tls == nil {
			r.endpoint = "http://" + r.endpoint
		}
		r.endpoint += setup.path
		r.httpServer = &http.Server{Handler: r, TLSConfig: setup.tls, ReadHeaderTimeout: 5 * time.Second}
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		protocols.SetUnencryptedHTTP2(setup.h2c)
		protocols.SetHTTP2(setup.http2)
		r.httpServer.Protocols = protocols
		if setup.tls != nil {
			if setup.http2 {
				setup.tls.NextProtos = []string{"h2", "http/1.1"}
			}
			listener = tls.NewListener(listener, setup.tls)
		}
		go func() { _ = r.httpServer.Serve(listener) }()
		return r
	}
	options := []grpc.ServerOption{grpc.StatsHandler(encodingHandler{r})}
	if setup.tls != nil {
		options = append(options, grpc.Creds(credentials.NewTLS(setup.tls)))
	}
	r.server = grpc.NewServer(options...)
	collogs.RegisterLogsServiceServer(r.server, r)
	colmetrics.RegisterMetricsServiceServer(r.server, metricsService{receiver: r})
	coltrace.RegisterTraceServiceServer(r.server, traceService{receiver: r})
	go func() { _ = r.server.Serve(listener) }()
	return r
}

// ServeHTTP is the HTTP side: protobuf exports on /v1/logs, /v1/metrics and /v1/traces,
// after any path the endpoint has.
func (r *receiver) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	logs := strings.HasSuffix(req.URL.Path, "/v1/logs")
	r.mu.Lock()
	r.seen = append(r.seen, seenRequest{
		logs:          logs,
		path:          req.URL.Path,
		contentType:   req.Header.Get("Content-Type"),
		encoding:      req.Header.Get("Content-Encoding"),
		authorization: req.Header.Values("Authorization"),
		header:        req.Header.Clone(),
		tls:           req.TLS != nil,
		clientCert:    req.TLS != nil && len(req.TLS.PeerCertificates) > 0,
		proto:         req.Proto,
	})
	var refuse *answer
	if logs && len(r.answers) > 0 {
		refuse = &r.answers[0]
		r.answers = r.answers[1:]
	}
	r.mu.Unlock()
	if refuse != nil {
		// Reading the body first keeps the connection usable. A reply sent
		// with the body unread makes the HTTP server close the connection
		// and wait half a second in a goroutine of its own, which outlives
		// the scenario and fails its check for leftover goroutines.
		_, _ = io.Copy(io.Discard, req.Body)
		if refuse.retryAfter != "" {
			rw.Header().Set("Retry-After", refuse.retryAfter)
		}
		rw.WriteHeader(refuse.status)
		return
	}
	body := io.Reader(req.Body)
	if req.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(req.Body)
		if err != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		body = unzipped
	}
	data, _ := io.ReadAll(body)
	var reply proto.Message
	switch {
	case logs:
		in := &collogs.ExportLogsServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		reply, _ = r.Export(req.Context(), in)
	case strings.HasSuffix(req.URL.Path, "/v1/traces"):
		in := &coltrace.ExportTraceServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		reply, _ = r.exportTraces(req.Context(), in)
	case strings.HasSuffix(req.URL.Path, "/v1/metrics"):
		in := &colmetrics.ExportMetricsServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		reply, _ = r.exportMetrics(req.Context(), in)
	default:
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	out, _ := proto.Marshal(reply)
	rw.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = rw.Write(out)
}

// sawGRPC remembers a gRPC export request.
func (r *receiver) sawGRPC(ctx context.Context, logs bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	seen := seenRequest{logs: logs, authorization: md.Get("authorization")}
	if p, ok := peer.FromContext(ctx); ok {
		if info, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			seen.tls = true
			seen.clientCert = len(info.State.PeerCertificates) > 0
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, seen)
}

func (r *receiver) requests() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := append([]seenRequest(nil), r.seen...)
	for i := range seen {
		if !r.http && i < len(r.encodings) {
			seen[i].encoding = r.encodings[i]
		}
	}
	return seen
}

// encodingHandler sees the compression of each gRPC export.
type encodingHandler struct{ *receiver }

func (encodingHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

func (h encodingHandler) HandleRPC(_ context.Context, s stats.RPCStats) {
	if in, ok := s.(*stats.InHeader); ok {
		h.mu.Lock()
		h.encodings = append(h.encodings, in.Compression)
		h.mu.Unlock()
	}
}

func (encodingHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

func (encodingHandler) HandleConn(context.Context, stats.ConnStats) {}

func (r *receiver) wait(ctx context.Context) {
	r.mu.Lock()
	hold, delay := r.hold, r.delay
	r.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func (r *receiver) Export(ctx context.Context, req *collogs.ExportLogsServiceRequest) (*collogs.ExportLogsServiceResponse, error) {
	if !r.http {
		r.sawGRPC(ctx, true)
	}
	r.mu.Lock()
	if r.hold != nil {
		r.logsHeld++
	}
	var refuse codes.Code
	if !r.http && len(r.grpcAnswers) > 0 {
		refuse, r.grpcAnswers = r.grpcAnswers[0], r.grpcAnswers[1:]
	}
	r.mu.Unlock()
	if refuse != codes.OK {
		return nil, status.Error(refuse, "the receiver refuses this export")
	}
	r.wait(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rl := range req.GetResourceLogs() {
		res := values(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				r.records = append(r.records, receivedRecord{
					eventName:      lr.GetEventName(),
					timestamp:      lr.GetTimeUnixNano(),
					severityText:   lr.GetSeverityText(),
					severityNumber: int32(lr.GetSeverityNumber()),
					resource:       res,
					attrs:          values(lr.GetAttributes()),
					traceID:        hex.EncodeToString(lr.GetTraceId()),
					spanID:         hex.EncodeToString(lr.GetSpanId()),
					flags:          lr.GetFlags(),
				})
			}
		}
	}
	return &collogs.ExportLogsServiceResponse{}, nil
}

type metricsService struct {
	colmetrics.UnimplementedMetricsServiceServer
	*receiver
}

func (m metricsService) Export(ctx context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	m.sawGRPC(ctx, false)
	return m.exportMetrics(ctx, req)
}

func (r *receiver) exportMetrics(ctx context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	r.wait(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		res := values(rm.GetResource().GetAttributes())
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				r.points = append(r.points, pointsOf(m)...)
				if m.GetHistogram() != nil || m.GetExponentialHistogram() != nil {
					r.histograms++
				}
				if m.GetName() != "hotline.requests" {
					r.otherMetric = append(r.otherMetric, m.GetName())
					continue
				}
				sum := m.GetSum()
				for _, dp := range sum.GetDataPoints() {
					r.counts = append(r.counts, receivedCount{
						unit:        m.GetUnit(),
						monotonic:   sum.GetIsMonotonic(),
						temporality: sum.GetAggregationTemporality(),
						resource:    res,
						attrs:       values(dp.GetAttributes()),
						value:       dp.GetAsInt(),
					})
				}
			}
		}
	}
	return &colmetrics.ExportMetricsServiceResponse{}, nil
}

// pointsOf are the data points of one metric, of the kinds the proxy sends.
func pointsOf(m *metrics.Metric) []receivedPoint {
	var points []receivedPoint
	number := func(kind string, dp *metrics.NumberDataPoint) {
		v := float64(dp.GetAsInt())
		if _, ok := dp.GetValue().(*metrics.NumberDataPoint_AsDouble); ok {
			v = dp.GetAsDouble()
		}
		points = append(points, receivedPoint{name: m.GetName(), unit: m.GetUnit(), description: m.GetDescription(), kind: kind, attrs: values(dp.GetAttributes()), value: v})
	}
	for _, dp := range m.GetSum().GetDataPoints() {
		number("sum", dp)
	}
	for _, dp := range m.GetGauge().GetDataPoints() {
		number("gauge", dp)
	}
	for _, dp := range m.GetHistogram().GetDataPoints() {
		points = append(points, receivedPoint{
			name: m.GetName(), unit: m.GetUnit(), description: m.GetDescription(), kind: "histogram", attrs: values(dp.GetAttributes()),
			value: dp.GetSum(), count: dp.GetCount(), bounds: dp.GetExplicitBounds(),
		})
	}
	return points
}

// metricPoints are every data point the receiver got, oldest first.
func (r *receiver) metricPoints() []receivedPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedPoint(nil), r.points...)
}

type traceService struct {
	coltrace.UnimplementedTraceServiceServer
	*receiver
}

func (t traceService) Export(ctx context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	t.sawGRPC(ctx, false)
	return t.exportTraces(ctx, req)
}

func (r *receiver) exportTraces(ctx context.Context, req *coltrace.ExportTraceServiceRequest) (*coltrace.ExportTraceServiceResponse, error) {
	r.wait(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	kinds := map[tracepb.Span_SpanKind]string{
		tracepb.Span_SPAN_KIND_SERVER: "server", tracepb.Span_SPAN_KIND_CLIENT: "client",
		tracepb.Span_SPAN_KIND_INTERNAL: "internal", tracepb.Span_SPAN_KIND_PRODUCER: "producer",
		tracepb.Span_SPAN_KIND_CONSUMER: "consumer",
	}
	statuses := map[tracepb.Status_StatusCode]string{
		tracepb.Status_STATUS_CODE_UNSET: "unset", tracepb.Status_STATUS_CODE_OK: "ok", tracepb.Status_STATUS_CODE_ERROR: "error",
	}
	for _, rs := range req.GetResourceSpans() {
		res := values(rs.GetResource().GetAttributes())
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				r.spanList = append(r.spanList, receivedSpan{
					traceID:  hex.EncodeToString(s.GetTraceId()),
					spanID:   hex.EncodeToString(s.GetSpanId()),
					parent:   hex.EncodeToString(s.GetParentSpanId()),
					name:     s.GetName(),
					kind:     kinds[s.GetKind()],
					start:    int64(s.GetStartTimeUnixNano()), //nolint:gosec // nanoseconds since 1970 fit until 2262
					end:      int64(s.GetEndTimeUnixNano()),   //nolint:gosec // as above
					status:   statuses[s.GetStatus().GetCode()],
					flags:    s.GetFlags(),
					attrs:    values(s.GetAttributes()),
					resource: res,
				})
			}
		}
	}
	return &coltrace.ExportTraceServiceResponse{}, nil
}

// spans are every span the receiver got, in the order they came.
func (r *receiver) spans() []receivedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedSpan(nil), r.spanList...)
}

// waitForLogsHeld waits until a log export is held.
func (r *receiver) waitForLogsHeld() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		held := r.logsHeld
		r.mu.Unlock()
		if held > 0 {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return failf("no log export arrived")
}

// release lets every held export go on.
func (r *receiver) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hold != nil {
		close(r.hold)
		r.hold = nil
	}
}

func (r *receiver) got() ([]receivedRecord, []receivedCount) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.records...), append([]receivedCount(nil), r.counts...)
}

func (r *receiver) stop() {
	r.release()
	if r.server != nil {
		r.server.Stop()
	}
	if r.httpServer != nil {
		_ = r.httpServer.Close()
	}
}

func values(kvs []*common.KeyValue) map[string]any {
	m := map[string]any{}
	for _, kv := range kvs {
		m[kv.GetKey()] = value(kv.GetValue())
	}
	return m
}

func value(v *common.AnyValue) any {
	switch x := v.GetValue().(type) {
	case *common.AnyValue_StringValue:
		return x.StringValue
	case *common.AnyValue_IntValue:
		return x.IntValue
	case *common.AnyValue_DoubleValue:
		return x.DoubleValue
	case *common.AnyValue_BoolValue:
		return x.BoolValue
	case *common.AnyValue_KvlistValue:
		return values(x.KvlistValue.GetValues())
	case *common.AnyValue_ArrayValue:
		var list []any
		for _, item := range x.ArrayValue.GetValues() {
			list = append(list, value(item))
		}
		return list
	}
	return nil
}
