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

	"hotline-proxy/internal/config"
)

// ErrRefused is a send the collector did not take.
var ErrRefused = errors.New("export: the collector refused the send")

// signal is what a send carries: records or spans.
type signal int

const (
	records signal = iota
	spans
)

// ErrBusy is what a queue's Send returns, wrapped or as it is, when the
// collector was too busy or could not be reached: the queue keeps the
// items and tries again later. Any other error refuses them for good.
var ErrBusy = errors.New("export: the collector is busy")

// busy is a refusal worth trying again, after at least wait.
type busy struct {
	wait time.Duration
	err  error
}

func (b busy) Error() string { return b.err.Error() }

// Is says a busy refusal is an ErrBusy.
func (busy) Is(target error) bool { return target == ErrBusy }

// sendWithRetry sends body, and tries again while the collector is busy and
// the send's time allows: first after about a second, then at most every
// few seconds, or later when the collector asks.
func sendWithRetry(ctx context.Context, t *httpTransport, k signal, z *zipper, body []byte, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backoff := retryFirst
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		err := t.post(ctx, k, z, body)
		var b busy
		if err == nil || !errors.As(err, &b) {
			return err
		}
		wait := max(backoff, b.wait)
		backoff = min(backoff*3/2, retryMost)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
			return err
		}
		// The wait fits in the send's time, checked above, unless the
		// send is cut short first, such as by a second stop signal.
		timer.Reset(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return err
		}
	}
}

// httpTransport sends one export request, already written as protobuf,
// over HTTP to /v1/logs or /v1/traces after the endpoint's own path. A busy
// collector's error is a busy, which says how long it asked to wait. Over TLS it uses HTTP/2 when the
// collector offers it; a plain endpoint uses HTTP/1.1, or HTTP/2 from the
// first byte when h2c is set (Peter, 2026-11-08).
type httpTransport struct {
	client  *http.Client
	urls    [2]string
	headers map[string]string
	agent   string
	// gzip says the exporter gzips: each sender then zips with its
	// own zipper, from newZipper.
	gzip bool
}

// zipper is a gzip writer and its output. Each sender owns one, as it owns
// its body buffer, since its sends come one at a time: the record queue,
// the span queue and the ledger's batch line each have their own. Two
// sends through one zipper at once mix their bodies, or crash.
type zipper struct {
	out bytes.Buffer
	w   *gzip.Writer
}

// newZipper is a zipper for one sender, or nil when the exporter does
// not gzip.
func (t *httpTransport) newZipper() *zipper {
	if !t.gzip {
		return nil
	}
	z := &zipper{}
	// BestSpeed is a valid level, so there is no error to handle.
	z.w, _ = gzip.NewWriterLevel(&z.out, gzip.BestSpeed)
	return z
}

// zip is body gzipped, in the zipper's output, until its next zip.
func (z *zipper) zip(body []byte) []byte {
	z.out.Reset()
	z.w.Reset(&z.out)
	// Writes to a bytes.Buffer do not fail.
	_, _ = z.w.Write(body)
	_ = z.w.Close()
	return z.out.Bytes()
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
	return t
}

// httpClient is the HTTP client of one exporter: HTTP/2 over TLS when
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

// post sends body, gzipped with z unless z is nil.
func (t *httpTransport) post(ctx context.Context, k signal, z *zipper, body []byte) error {
	if z != nil {
		body = z.zip(body)
	}
	// The config has checked the URL, the only thing that could fail here.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, t.urls[k], bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("User-Agent", t.agent)
	if z != nil {
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
	// Reading the response keeps the connection for the next send. Its
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
