package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"hotline-proxy/internal/config"
)

// ErrRefused is a send the collector did not take.
var ErrRefused = errors.New("export: the collector refused the send")

// kind is what a send carries: records or spans.
type kind int

const (
	records kind = iota
	spans
)

// transport sends one export request, already written as protobuf, to a
// destination. A busy collector's error is a busy, which says how long it
// asked to wait.
type transport interface {
	post(ctx context.Context, k kind, body []byte) error
	close()
}

// busy is a refusal worth trying again, after at least wait.
type busy struct {
	wait time.Duration
	err  error
}

func (b busy) Error() string { return b.err.Error() }

// sendWithRetry sends body, and tries again while the collector is busy and
// the send's time allows: first after about a second, then at most every
// few seconds, or later when the collector asks.
func sendWithRetry(ctx context.Context, t transport, k kind, body []byte, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backoff := retryFirst
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		err := t.post(ctx, k, body)
		var b busy
		if err == nil || !errors.As(err, &b) {
			return err
		}
		wait := max(backoff, b.wait)
		backoff = min(backoff*3/2, retryMost)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
			return err
		}
		// The wait fits in the send's time, checked above.
		timer.Reset(wait)
		<-timer.C
	}
}

// httpTransport sends over HTTP as protobuf, to /v1/logs and /v1/traces
// after the endpoint's own path. Over TLS it uses HTTP/2 when the
// collector offers it; a plain endpoint uses HTTP/1.1, or HTTP/2 from the
// first byte when h2c is set (Peter, 2026-11-08).
type httpTransport struct {
	client  *http.Client
	urls    [2]string
	headers map[string]string
	agent   string
	gzip    bool
	// zips are a gzip writer and its output for each kind, each used by
	// one queue's sends, one at a time.
	zips [2]zipper
}

type zipper struct {
	out bytes.Buffer
	w   *gzip.Writer
}

func newHTTPTransport(e config.Exporter, tlsConfig *tls.Config, version string) *httpTransport {
	base := e.URL.Scheme + "://" + e.URL.Host + strings.TrimSuffix(e.URL.Path, "/")
	t := &httpTransport{
		client:  httpClient(e, tlsConfig),
		urls:    [2]string{base + "/v1/logs", base + "/v1/traces"},
		headers: e.Headers,
		agent:   "hotline-proxy/" + version,
		gzip:    e.Gzip,
	}
	for i := range t.zips {
		t.zips[i].w, _ = gzip.NewWriterLevel(&t.zips[i].out, gzip.BestSpeed)
	}
	return t
}

// httpClient is the HTTP client of one destination: HTTP/2 over TLS when
// the collector offers it, and h2c to a plain endpoint when set.
func httpClient(e config.Exporter, tlsConfig *tls.Config) *http.Client {
	rt := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if e.H2C {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		rt.Protocols = protocols
	}
	return &http.Client{Transport: rt}
}

func (t *httpTransport) post(ctx context.Context, k kind, body []byte) error {
	if t.gzip {
		z := &t.zips[k]
		z.out.Reset()
		z.w.Reset(&z.out)
		// Writes to a bytes.Buffer do not fail.
		_, _ = z.w.Write(body)
		_ = z.w.Close()
		body = z.out.Bytes()
	}
	// The config has checked the URL, the only thing that could fail here.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, t.urls[k], bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("User-Agent", t.agent)
	if t.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	for name, value := range t.headers {
		req.Header.Set(name, value)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		// The collector could not be reached: it may be starting.
		return busy{err: fmt.Errorf("%w: %w", ErrRefused, err)}
	}
	// Reading the reply keeps the connection for the next send. Its
	// partial success, if any, is not acted on.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusBadGateway,
		resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusGatewayTimeout:
		wait := time.Duration(0)
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return busy{wait: wait, err: fmt.Errorf("%w: HTTP status %d", ErrRefused, resp.StatusCode)}
	default:
		return fmt.Errorf("%w: HTTP status %d", ErrRefused, resp.StatusCode)
	}
}

func (t *httpTransport) close() { t.client.CloseIdleConnections() }

// grpcTransport sends over gRPC, to the standard OTLP services, with the
// written bytes as the message.
type grpcTransport struct {
	conn    *grpc.ClientConn
	headers metadata.MD
	options []grpc.CallOption
}

// grpcMethod is the OTLP service that takes records or spans.
func grpcMethod(k kind) string {
	if k == spans {
		return "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
	}
	return "/opentelemetry.proto.collector.logs.v1.LogsService/Export"
}

// newGRPCTransport connects on the first send. The config has checked the
// endpoint, the only thing that could fail here.
func newGRPCTransport(e config.Exporter, tlsConfig *tls.Config, version string) *grpcTransport {
	creds := insecure.NewCredentials()
	if tlsConfig != nil {
		creds = credentials.NewTLS(tlsConfig)
	}
	conn, _ := grpc.NewClient(e.URL.Host,
		grpc.WithTransportCredentials(creds),
		grpc.WithUserAgent("hotline-proxy/"+version),
		grpc.WithDefaultCallOptions(grpc.ForceCodecV2(rawCodec{})))
	t := &grpcTransport{conn: conn, headers: metadata.New(e.Headers)}
	if e.Gzip {
		t.options = append(t.options, grpc.UseCompressor(grpcgzip.Name))
	}
	return t
}

func (t *grpcTransport) post(ctx context.Context, k kind, body []byte) error {
	ctx = metadata.NewOutgoingContext(ctx, t.headers)
	var reply []byte
	err := t.conn.Invoke(ctx, grpcMethod(k), &body, &reply, t.options...)
	if err == nil {
		return nil
	}
	s := status.Convert(err)
	refused := fmt.Errorf("%w: gRPC %s", ErrRefused, s.Code())
	switch s.Code() {
	case codes.Canceled, codes.DeadlineExceeded, codes.Aborted, codes.OutOfRange, codes.Unavailable, codes.DataLoss:
		return busy{wait: retryDelay(s), err: refused}
	case codes.ResourceExhausted:
		// Only a collector that says when to come back is busy; any
		// other means the send is too large.
		if wait := retryDelay(s); wait > 0 {
			return busy{wait: wait, err: refused}
		}
	}
	return refused
}

// retryDelay is how long the collector asked to wait, or 0.
func retryDelay(s *status.Status) time.Duration {
	for _, d := range s.Details() {
		if info, ok := d.(*errdetails.RetryInfo); ok {
			return info.GetRetryDelay().AsDuration()
		}
	}
	return 0
}

func (t *grpcTransport) close() { _ = t.conn.Close() }

// rawCodec hands gRPC the written bytes as they are, and drops the reply,
// whose partial success, if any, is not acted on.
type rawCodec struct{}

// Marshal takes the *[]byte the transport always passes.
func (rawCodec) Marshal(v any) (mem.BufferSlice, error) {
	body, _ := v.(*[]byte)
	return mem.BufferSlice{mem.SliceBuffer(*body)}, nil
}

func (rawCodec) Unmarshal(mem.BufferSlice, any) error { return nil }

func (rawCodec) Name() string { return "proto" }
