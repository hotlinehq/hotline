package proxy

import (
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/tracectx"
)

// session is a WebSocket after its 101: the bytes each side sends, counted
// and hashed as they pass, and when it opened. The opening call is recorded
// as soon as the 101 reaches the app, and the session in a record of its
// own when it ends, in the call's trace, under the call's span (Peter,
// 2026-11-18, WebSocket milestone 1). It reads no frames.
type session struct {
	// span is the session span's id, which the session record and its
	// hashes carry.
	span trace.SpanID
	// fromVendor reads the vendor's side, and fromApp writes the app's
	// side to the vendor.
	fromVendor *tap
	fromApp    *countedWriter
	// opened is when the 101 reached the app; zero while it has not.
	opened time.Time
}

// newSession sets up the session of a call whose vendor answered 101:
// vendor is the vendor's side, read and written.
func (c *call) newSession(vendor io.ReadCloser, toVendor io.Writer) *session {
	span := tracectx.NewSpanID()
	return &session{
		span:       span,
		fromVendor: &tap{ReadCloser: vendor, mac: c.h.bodyHash("from_vendor", c.trace.WithSpanID(span))},
		fromApp:    &countedWriter{Writer: toVendor, mac: c.h.bodyHash("from_app", c.trace.WithSpanID(span))},
	}
}

// Switched is called once the 101 has reached the app: the opening call
// ends there and is recorded now, while the session goes on. The proxy
// keeps the session, so a stop can end it.
func (c *call) Switched() {
	at := c.h.clock.Now()
	if c.api != nil {
		// A side not yet checked is checked now, as at the end of any call.
		c.checkRequest(at)
	}
	c.mark(func() { c.session.opened = at })
	c.h.sessions.Store(c, struct{}{})
	c.h.recordLater(func() finished { return c.record(at) })
}

// sessionRecord builds the session's record and span, once it has ended.
func (c *call) sessionRecord(end time.Time) finished {
	c.mu.Lock()
	s := c.session
	opened := s.opened
	c.mu.Unlock()
	fromApp, appHash := s.fromApp.counts()
	fromVendor, _ := s.fromVendor.counts()
	sc := c.trace.WithSpanID(s.span)
	// Room for the ledger's four, as for a call's record.
	attrs := make([]attribute.KeyValue, 0, 16)
	attrs = append(attrs,
		attribute.String("hotline.integration.id", orNotPassed(c.id)),
		attribute.String("hotline.ingress", c.ingress),
		clientAddress(c.in),
		semconv.ServerAddress(c.host),
	)
	attrs = append(attrs, c.process.attributes()...)
	attrs = append(attrs,
		attribute.Int64("hotline.websocket.from_app.size", fromApp),
		attribute.Int64("hotline.websocket.from_vendor.size", fromVendor),
		attribute.Int64("hotline.websocket.duration_us", micros(opened, end)),
		attribute.String("hotline.websocket.from_app.hash", appHash),
		attribute.String("hotline.websocket.from_vendor.hash", s.fromVendor.hashed()),
	)
	span := export.Span{
		ID: s.span, Parent: c.trace.SpanID(), Name: "websocket session", Kind: trace.SpanKindInternal,
		Start: opened, End: end,
	}
	return finished{
		event: export.SessionEvent, cfg: c.cfg, chain: c.integration.Name, sc: sc, at: opened,
		attrs: attrs, spans: []export.Span{span},
	}
}

// endSessions ends every WebSocket still open, so each is recorded as the
// proxy stops instead of being dropped.
func (h *handler) endSessions() {
	h.sessions.Range(func(c, _ any) bool {
		c.(*call).cancel()
		return true
	})
}

// countedWriter is the app's side of a session on its way to the vendor:
// it counts and hashes every byte written.
type countedWriter struct {
	io.Writer
	mac *ledger.BodyHash

	mu   sync.Mutex
	size int64
}

func (w *countedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.mu.Lock()
	w.size += int64(n)
	_, _ = w.mac.Write(p[:n])
	w.mu.Unlock()
	return n, err
}

// counts is how many bytes were written, and their hash.
func (w *countedWriter) counts() (int64, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size, w.mac.Sum()
}
