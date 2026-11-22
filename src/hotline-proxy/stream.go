package proxy

import (
	"io"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/config"
	"hotline-proxy/internal/export"
	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// responseStream is a streamed response whose spec gives an itemSchema: its stream items,
// read as they pass. As for a WebSocket, the call is recorded as soon as
// the response's headers reach the app, each stream item in a record of its own as
// it ends, and the stream in a record of its own when it ends, in the
// call's trace, under the call's span (Peter, 2026-11-21, M2, Q1 to Q3).
type responseStream struct {
	c *call
	// span is the stream span's id, which the stream record, its hash and
	// every stream item's span carry.
	span       trace.SpanID
	itemSchema *spec.ItemSchema
	// checked says the api's checks hold the response body, so each stream item
	// is checked.
	checked bool
	// bytes is what the vendor sends after the headers, counted and hashed
	// with the stream's span as it passes on to the app.
	bytes *tap
	// read cuts the bytes into stream items, by the response's media type;
	// only the tap that passes them uses it.
	read      streamItemReader
	itemCount int64
	// opened is when the headers reached the app; zero while they have not.
	// appLeft says the app was gone by then: the call record says so,
	// while the stream goes on writing to the app.
	opened  time.Time
	appLeft bool
	// recording counts the stream item records not yet made, and invalid the
	// stream items whose check found a problem, for the stream record.
	recording sync.WaitGroup
	invalid   atomic.Int64
	// called closes once the call's record is handed on: every stream item record
	// waits for it, and the stream record for them, so a reader gets the
	// call first and the stream last. The forwarder sends a stream's
	// headers before it reads a byte of the body, so no stream item comes before
	// the stream opened.
	called chan struct{}
}

// newStream sets up the stream of a response that has stream items in the spec, and
// the tap its bytes pass through.
func (c *call) newStream(itemSchema *spec.ItemSchema, header http.Header, body io.ReadCloser) *responseStream {
	s := &responseStream{
		c: c, span: tracectx.NewSpanID(), itemSchema: itemSchema, checked: c.api.Checks.Has(config.PartResponseBody),
		called: make(chan struct{}),
	}
	s.bytes = &tap{
		ReadCloser: body, mac: c.h.bodyHash("stream", c.trace.WithSpanID(s.span)), stream: s,
		saver: bodySaver{limit: saved(c.integration), expect: -1},
	}
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	s.read = newStreamItemReader(mediaType, s.checkStreamItem)
	return s
}

// pass reads the bytes of the response as they pass on to the app.
func (s *responseStream) pass(p []byte) {
	if len(p) > 0 {
		s.read.pass(p, s.c.h.clock.Now())
	}
}

// endedWhole says the vendor ended the stream whole, so a stream item left open
// is read as its format says.
func (s *responseStream) endedWhole() { s.read.end() }

// streamOpened is called once the response's headers have reached the app: the call
// ends there and is recorded now, while the stream goes on. The proxy keeps
// the stream, so a stop can end it.
func (c *call) streamOpened() {
	at := c.h.clock.Now()
	c.mu.Lock()
	resp := c.resp
	c.vendorOut = at
	c.stream.opened, c.stream.appLeft = at, c.app.failed()
	c.mu.Unlock()
	c.checkRequest(at)
	c.checkResponse(at, resp)
	c.h.streams.Store(c, struct{}{})
	if c.h.streamsEnded.Load() {
		c.cancel()
	}
	c.h.recordThen(func() finished { return c.record(at) }, func() { close(c.stream.called) })
}

// appFailed says the app did not get the whole response. A stream's call
// ends with the headers, so for it this is how things stood then. It
// reads the call, which is locked.
func (c *call) appFailed() bool {
	if c.stream != nil && !c.stream.opened.IsZero() {
		return c.stream.appLeft
	}
	return c.app.failed()
}

// endStreams ends every streamed response still open, and any that opens
// from now on, so each is recorded as the proxy stops. A stream holds its
// connection to the app busy, so the servers' stop would wait for it
// until the time to stop runs out.
func (h *handler) endStreams() {
	h.streamsEnded.Store(true)
	h.streams.Range(func(c, _ any) bool {
		c.(*call).cancel()
		return true
	})
}

// checkStreamItem starts the check of one stream item, and records it once the check ends.
func (s *responseStream) checkStreamItem(e streamItem) {
	s.itemCount++
	index := s.itemCount
	var check *specCheck
	if s.checked && e.asJSON != nil {
		check = newSpecCheck()
		check.start = e.end
		check.started.Store(true)
		s.c.h.checks.add(&checkJob{
			check: check,
			run:   func() (spec.Checked, spec.Route) { return s.itemSchema.Check(e.asJSON), spec.Route{} },
			bytes: int64(len(e.asJSON)),
		})
	}
	s.recording.Add(1)
	// The stream item is done once its record is handed on, so the stream
	// record, which waits for every stream item, always comes after them.
	s.c.h.recordThen(func() finished {
		<-s.called
		return s.record(index, e, check)
	}, s.recording.Done)
}

// record builds a stream item's record and its span, with the check's step under
// it.
func (s *responseStream) record(index int64, e streamItem, check *specCheck) finished {
	var found spec.Checked
	ran := s.checked
	switch {
	case check != nil:
		check.wait()
		found = check.found
	case s.checked:
		found.Skipped = "body_too_large"
	}
	if len(found.Response) > 0 {
		s.invalid.Add(1)
	}
	c := s.c
	id := tracectx.NewSpanID()
	attrs := make([]attribute.KeyValue, 0, 12)
	attrs = append(attrs,
		attribute.String("hotline.integration.id", orNotPassed(c.id)),
		attribute.String("hotline.ingress", c.ingress),
		attribute.Int64("hotline.stream.item.index", index),
		attribute.Int64("hotline.stream.item.size", e.size),
	)
	attrs = checked(attrs, found, ran)
	spans := []export.Span{{
		ID: id, Parent: s.span, Name: "stream item", Kind: trace.SpanKindInternal, Start: e.start, End: e.end,
	}}
	if check != nil {
		spans = append(spans, c.checkSpans("item spec check", id, check, found.Response, found.Skipped)...)
	}
	return finished{
		event: export.StreamItemEvent, cfg: c.cfg, chain: c.integration.Name, sc: c.trace.WithSpanID(id),
		at: e.start, attrs: attrs, spans: spans,
	}
}

// streamRecord builds the stream's record and span once it has ended and
// every stream item is recorded: its stream items, how many broke the spec, its bytes,
// their hash, and whether it ended whole or was cut, by the vendor, the app
// or a stop.
func (c *call) streamRecord(end time.Time) finished {
	s := c.stream
	s.recording.Wait()
	c.mu.Lock()
	opened := s.opened
	c.mu.Unlock()
	size, err := s.bytes.counts()
	how := "complete"
	if err != nil || c.app.failed() {
		how = "cut"
	}
	attrs := make([]attribute.KeyValue, 0, 16)
	attrs = append(attrs,
		attribute.String("hotline.integration.id", orNotPassed(c.id)),
		attribute.String("hotline.ingress", c.ingress),
		clientAddress(c.in),
		semconv.ServerAddress(c.host),
	)
	attrs = append(attrs, c.process.attributes()...)
	attrs = append(attrs,
		attribute.Int64("hotline.stream.items", s.itemCount),
		attribute.Int64("hotline.stream.items_invalid", s.invalid.Load()),
		attribute.Int64("hotline.stream.size", size),
		attribute.Int64("hotline.stream.duration_us", micros(opened, end)),
		attribute.String("hotline.stream.hash", s.bytes.hashed()),
		attribute.String("hotline.stream.end", how),
	)
	span := export.Span{
		ID: s.span, Parent: c.trace.SpanID(), Name: "stream", Kind: trace.SpanKindInternal,
		Start: opened, End: end, Status: export.FailedIf(how == "cut"),
	}
	var content []attribute.KeyValue
	if c.integration.Content.Full {
		var hash attribute.KeyValue
		content, hash = s.content()
		attrs = append(attrs, hash)
	}
	return finished{
		event: export.StreamEvent, cfg: c.cfg, chain: c.integration.Name, sc: c.trace.WithSpanID(s.span), at: opened,
		attrs: attrs, spans: []export.Span{span}, content: content,
	}
}

// content is what content: full adds to the stream record for the file
// exporters: the first maxBodySize bytes of the stream, as bytes, and a
// flag when the stream was longer. As for a call, the record's
// hotline.content.hash covers the content but the body, which
// hotline.stream.hash covers already.
func (s *responseStream) content() (content []attribute.KeyValue, hash attribute.KeyValue) {
	body, cut := s.bytes.savedBody()
	if cut {
		content = append(content, attribute.Bool("hotline.content.stream.body.truncated", true))
	}
	hash = attribute.String("hotline.content.hash", ledger.Text(s.c.h.hashes.Content(content)))
	if len(body) > 0 {
		content = append(content, attribute.ByteSlice("hotline.content.stream.body", body))
	}
	return content, hash
}
