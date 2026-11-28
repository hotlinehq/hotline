package proxy_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// receivedRecord is one log record the fake OTLP receiver got.
type receivedRecord struct {
	eventName string
	// timestamp and observed are the record's time and the time it was
	// written, in nanoseconds since 1970.
	timestamp uint64
	observed  uint64
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
	// body is the record's text, when it is one.
	body string
	// fields are the numbers of the LogRecord fields the proxy set, and
	// unknown the bytes of fields this version of OTLP does not name; scope
	// is the name of the scope the record came in.
	fields  []int
	unknown int
	scope   string
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
	// links are the spans this one links to, each as trace id and span id
	// in hex, joined by a slash, and linkFlags their flags, in the same order.
	links     []string
	linkFlags []uint32
}

// receivedPoint is one data point of any metric the receiver got.
type receivedPoint struct {
	name string
	unit string
	// description is the metric's help text.
	description string
	// kind is sum, gauge or histogram.
	kind string
	// temporality says whether a sum is a running total or a change.
	temporality metrics.AggregationTemporality
	attrs       map[string]any
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
	// path goes in front of /v1/logs, /v1/metrics and /v1/traces.
	path string
	// tls, when set, makes the receiver accept only TLS, with this setup.
	tls *tls.Config
	// listener, when set, is where the receiver listens.
	listener net.Listener
	// http2 offers HTTP/2 over TLS; h2c takes HTTP/2 on a plain port.
	http2 bool
	h2c   bool
}

// receiver is a fake OTLP exporter over HTTP on a local port.
type receiver struct {
	httpServer *http.Server
	// endpoint is how the config names the receiver.
	endpoint string

	mu      sync.Mutex
	records []receivedRecord
	// taken counts every call record ever got, also those forget dropped.
	taken   int
	batches []receivedRecord
	// drops are records with the event of the drop records the ledger
	// sent before each exporter signed its own batch record, kept apart so
	// a scenario can check that none comes any more.
	drops []receivedRecord
	// sessions are the WebSocket session records, kept apart from the
	// calls' records, as the batches are.
	sessions []receivedRecord
	// messages are the WebSocket message records, kept apart too.
	messages []receivedRecord
	// streams and items are the records of streamed responses and of their
	// items, kept apart as well.
	streams []receivedRecord
	items   []receivedRecord
	// probes are the records of the probes of vendors, kept apart too.
	probes []receivedRecord
	// busy answers every log and trace export with 503, until it is back.
	busy bool
	// refused counts the call records refused while busy.
	refused  int
	points   []receivedPoint
	spanList []receivedSpan
	// logsHeld counts log exports that are waiting on hold.
	logsHeld int
	// hold, while open, keeps every export waiting.
	hold chan struct{}
	// delay slows every export down.
	delay time.Duration
	// seen are the export requests, in the order they came.
	seen []seenRequest
	// answers are given to the first log exports, one each.
	answers []answer
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
	r := &receiver{endpoint: "http://" + listener.Addr().String()}
	if setup.tls != nil {
		r.endpoint = "https://localhost:" + port
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

// answerNextLogExports gives the next log exports these answers, one each,
// after any it was given before.
func (r *receiver) answerNextLogExports(answers ...answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, answers...)
}

// ServeHTTP takes protobuf exports on /v1/logs, /v1/metrics and /v1/traces,
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
	// A busy collector refuses every export; the records it refused are
	// counted, so a test can tell they were sent.
	busy := r.busy && (logs || strings.HasSuffix(req.URL.Path, "/v1/traces"))
	switch {
	case busy:
		refuse = &answer{status: http.StatusServiceUnavailable}
	case logs && len(r.answers) > 0:
		refuse = &r.answers[0]
		r.answers = r.answers[1:]
	}
	r.mu.Unlock()
	if refuse != nil {
		// Reading the body first keeps the connection usable. A response sent
		// with the body unread makes the HTTP server close the connection
		// and wait half a second in a goroutine of its own, which outlives
		// the scenario and fails its check for leftover goroutines.
		data, _ := io.ReadAll(req.Body)
		if busy && logs {
			r.countRefused(req.Header.Get("Content-Encoding"), data)
		}
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
	var response proto.Message
	switch {
	case logs:
		in := &collogs.ExportLogsServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		response, _ = r.Export(req.Context(), in)
	case strings.HasSuffix(req.URL.Path, "/v1/traces"):
		in := &coltrace.ExportTraceServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		response, _ = r.exportTraces(req.Context(), in)
	case strings.HasSuffix(req.URL.Path, "/v1/metrics"):
		in := &colmetrics.ExportMetricsServiceRequest{}
		if proto.Unmarshal(data, in) != nil {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		response, _ = r.exportMetrics(req.Context(), in)
	default:
		rw.WriteHeader(http.StatusNotFound)
		return
	}
	out, _ := proto.Marshal(response)
	rw.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = rw.Write(out)
}

func (r *receiver) requests() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenRequest(nil), r.seen...)
}

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
	r.mu.Lock()
	if r.hold != nil {
		r.logsHeld++
	}
	r.mu.Unlock()
	r.wait(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rl := range req.GetResourceLogs() {
		res := values(rl.GetResource().GetAttributes())
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				list := &r.records
				// The ledger's batch records and the WebSocket session
				// records are kept apart, so the scenarios about calls see
				// only the calls' records.
				switch lr.GetEventName() {
				case "hotline.ledger.batch":
					list = &r.batches
				case "hotline.ledger.dropped":
					list = &r.drops
				case "hotline.websocket.session":
					list = &r.sessions
				case "hotline.websocket.message":
					list = &r.messages
				case "hotline.stream":
					list = &r.streams
				case "hotline.stream.item":
					list = &r.items
				case "hotline.probe":
					list = &r.probes
				default:
					r.taken++
				}
				*list = append(*list, receivedRecord{
					eventName:      lr.GetEventName(),
					timestamp:      lr.GetTimeUnixNano(),
					observed:       lr.GetObservedTimeUnixNano(),
					fields:         setFields(lr),
					unknown:        len(lr.ProtoReflect().GetUnknown()),
					scope:          sl.GetScope().GetName(),
					severityText:   lr.GetSeverityText(),
					severityNumber: int32(lr.GetSeverityNumber()),
					resource:       res,
					attrs:          values(lr.GetAttributes()),
					traceID:        hex.EncodeToString(lr.GetTraceId()),
					spanID:         hex.EncodeToString(lr.GetSpanId()),
					body:           lr.GetBody().GetStringValue(),
					flags:          lr.GetFlags(),
				})
			}
		}
	}
	return &collogs.ExportLogsServiceResponse{}, nil
}

func (r *receiver) exportMetrics(ctx context.Context, req *colmetrics.ExportMetricsServiceRequest) (*colmetrics.ExportMetricsServiceResponse, error) {
	r.wait(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				r.points = append(r.points, pointsOf(m)...)
			}
		}
	}
	return &colmetrics.ExportMetricsServiceResponse{}, nil
}

// pointsOf are the data points of one metric, of the kinds the proxy sends.
func pointsOf(m *metrics.Metric) []receivedPoint {
	var points []receivedPoint
	temporality := m.GetSum().GetAggregationTemporality()
	number := func(kind string, dp *metrics.NumberDataPoint) {
		v := float64(dp.GetAsInt())
		if _, ok := dp.GetValue().(*metrics.NumberDataPoint_AsDouble); ok {
			v = dp.GetAsDouble()
		}
		points = append(points, receivedPoint{name: m.GetName(), unit: m.GetUnit(), description: m.GetDescription(), kind: kind, temporality: temporality, attrs: values(dp.GetAttributes()), value: v})
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
				var links []string
				var linkFlags []uint32
				for _, l := range s.GetLinks() {
					links = append(links, hex.EncodeToString(l.GetTraceId())+"/"+hex.EncodeToString(l.GetSpanId()))
					linkFlags = append(linkFlags, l.GetFlags())
				}
				r.spanList = append(r.spanList, receivedSpan{
					links:     links,
					linkFlags: linkFlags,
					traceID:   hex.EncodeToString(s.GetTraceId()),
					spanID:    hex.EncodeToString(s.GetSpanId()),
					parent:    hex.EncodeToString(s.GetParentSpanId()),
					name:      s.GetName(),
					kind:      kinds[s.GetKind()],
					start:     int64(s.GetStartTimeUnixNano()), //nolint:gosec // nanoseconds since 1970 fit until 2262
					end:       int64(s.GetEndTimeUnixNano()),   //nolint:gosec // as above
					status:    statuses[s.GetStatus().GetCode()],
					flags:     s.GetFlags(),
					attrs:     values(s.GetAttributes()),
					resource:  res,
				})
			}
		}
	}
	return &coltrace.ExportTraceServiceResponse{}, nil
}

// spans are every span the receiver got, in the order they came.
// sessionRecords are the WebSocket session records the receiver got.
func (r *receiver) messageRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.messages...)
}

func (r *receiver) sessionRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.sessions...)
}

func (r *receiver) streamRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.streams...)
}

func (r *receiver) streamItemRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.items...)
}

// probeRecords are the probe records the receiver got, in the order they
// came.
func (r *receiver) probeRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.probes...)
}

func (r *receiver) spans() []receivedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedSpan(nil), r.spanList...)
}

// waitForLogsHeld waits until a log export is held.
func (r *receiver) waitForLogsHeld() error {
	held := eventually(waitLimit, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.logsHeld > 0
	})
	if !held {
		return failf("no log export arrived")
	}
	return nil
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

// ledger are the batch records the receiver got.
// countRefused counts the call records in a log export refused while down,
// so a scenario can still wait until the proxy has made them.
func (r *receiver) countRefused(encoding string, data []byte) {
	if encoding == "gzip" {
		unzipped, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		data, _ = io.ReadAll(unzipped)
	}
	in := &collogs.ExportLogsServiceRequest{}
	if proto.Unmarshal(data, in) != nil {
		return
	}
	n := 0
	for _, rl := range in.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				if lr.GetEventName() != "hotline.ledger.batch" {
					n++
				}
			}
		}
	}
	r.mu.Lock()
	r.refused += n
	r.mu.Unlock()
}

// callsMade counts the call records the receiver got or refused.
func (r *receiver) callsMade() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.taken + r.refused
}

// takenCalls counts every call record the receiver ever got, also those
// forget dropped since.
func (r *receiver) takenCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.taken
}

// forget drops what the receiver kept, so a long fuzz run does not fill
// the memory. It keeps the numbers of calls taken and refused.
func (r *receiver) forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records, r.batches, r.drops, r.sessions, r.messages, r.points = nil, nil, nil, nil, nil, nil
	r.streams, r.items = nil, nil
	r.spanList, r.seen = nil, nil
}

func (r *receiver) ledger() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.batches...)
}

// dropRecords are the records with the old drop records' event the
// receiver got.
func (r *receiver) dropRecords() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.drops...)
}

// setFields are the numbers of the fields a message holds, in order.
func setFields(m proto.Message) []int {
	var numbers []int
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		numbers = append(numbers, int(fd.Number()))
		return true
	})
	slices.Sort(numbers)
	return numbers
}

func (r *receiver) got() []receivedRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]receivedRecord(nil), r.records...)
}

func (r *receiver) stop() {
	r.release()
	// A receiver for an exporter that is down never started a server.
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

func (r *receiver) setBusy(busy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = busy
}
