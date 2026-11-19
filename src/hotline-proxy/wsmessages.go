package proxy

import (
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"hotline-proxy/internal/export"
	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/tracectx"
)

// reading is how a session whose route lists its messages reads them: one
// wire each way, and what the session record says about them. Each message
// gets a record and a span of its own (Peter, 2026-11-19, WebSocket slice
// 2, Q1 and Q21).
type reading struct {
	c *call
	// session is the session span's id, the parent of each message span.
	session  trace.SpanID
	messages *spec.Messages
	// checked says the api's checks hold messages (Q27).
	checked     bool
	next        atomic.Int64
	app, vendor *wire

	// closing says the proxy closed the vendor's side, and vendorFirst that
	// the vendor's side ended before that.
	closing     atomic.Bool
	vendorFirst atomic.Bool

	mu sync.Mutex
	// shut is the session's first close message, nil while none came.
	shut *closeMessage
	// unreadFrom is the index of the first message that could not be read,
	// 0 when every one was.
	unreadFrom int64
}

// closeMessage is a close message: who sent it, its code and its reason.
type closeMessage struct {
	fromApp bool
	code    int
	reason  string
}

func newReading(c *call, session trace.SpanID, messages *spec.Messages, checked bool, app, vendor *inflater) *reading {
	r := &reading{c: c, session: session, messages: messages, checked: checked}
	r.app = newWire(r, true, app)
	r.vendor = newWire(r, false, vendor)
	return r
}

// end says the session ended: both wires read what is left, and stop.
func (r *reading) end() {
	r.app.end()
	r.vendor.end()
}

// message names a whole message, starts its check, and records it once
// the check ends. Its name and ids are found here, in a step never dropped
// (Q14).
func (r *reading) message(w *wire, seen seenMessage, m message) {
	named := r.messages.Name(w.fromApp, m.data)
	body := spec.Body{Data: m.data, TooLarge: m.unpacked > int64(len(m.data))}
	var check *specCheck
	if r.checked && named.Name != spec.Unknown && !body.TooLarge {
		check = newSpecCheck()
		check.start = seen.end
		check.started.Store(true)
		r.c.h.checks.add(&checkJob{
			check: check,
			run: func() (spec.Checked, spec.Route) {
				return r.messages.Check(named, w.fromApp, m.data), spec.Route{}
			},
			bytes: int64(len(m.data)),
		})
	}
	r.c.h.recordLater(func() finished {
		return r.record(w, seen, &m, named, check, "")
	})
}

// unread records a message the wire could not read, as overloaded when
// its copy did not fit in memory (Q26).
func (r *reading) unread(w *wire, seen seenMessage, reason string) {
	r.mu.Lock()
	if r.unreadFrom == 0 || seen.index < r.unreadFrom {
		r.unreadFrom = seen.index
	}
	r.mu.Unlock()
	r.c.h.recordLater(func() finished {
		return r.record(w, seen, nil, spec.Message{Name: spec.Unknown}, nil, reason)
	})
}

// record builds a message's record and its span, with the check's step
// under it. m is nil for a message that was not read.
func (r *reading) record(w *wire, seen seenMessage, m *message, named spec.Message, check *specCheck, skipped string) finished {
	var found spec.Checked
	ran := true
	switch {
	case check != nil:
		check.wait()
		found = check.found
	case !r.checked:
		// A part left out of checks leaves no result and no reason (Q27).
		ran = false
	case skipped != "":
		found.Skipped = skipped
	case named.Skipped != "":
		found.Skipped = named.Skipped
	case named.Name != spec.Unknown:
		found.Skipped = "body_too_large"
	default:
		ran = false
	}
	c := r.c
	id := tracectx.NewSpanID()
	direction := "from_vendor"
	if w.fromApp {
		direction = "from_app"
	}
	attrs := make([]attribute.KeyValue, 0, 16)
	attrs = append(attrs,
		attribute.String("hotline.integration.id", orNotPassed(c.id)),
		attribute.String("hotline.ingress", c.ingress),
		attribute.Int64("hotline.websocket.message.index", seen.index),
		attribute.String("hotline.websocket.message.direction", direction),
		attribute.String("hotline.websocket.message.name", named.Name),
	)
	if m != nil {
		attrs = append(attrs, attribute.Int64("hotline.websocket.message.size", m.size))
		if m.packed {
			attrs = append(attrs, attribute.Int64("hotline.websocket.message.unpacked_size", m.unpacked))
		}
	}
	if len(named.IDs) > 0 {
		ids := named.IDs
		if len(ids) > shownResults {
			attrs = append(attrs, attribute.Int("hotline.websocket.ids_total", len(ids)))
			ids = ids[:shownResults]
		}
		attrs = append(attrs, attribute.StringSlice("hotline.websocket.ids", ids))
	}
	attrs = checked(attrs, found, ran)
	spans := []export.Span{{
		ID: id, Parent: r.session, Name: "websocket message", Kind: trace.SpanKindInternal,
		Start: seen.start, End: seen.end,
	}}
	if check != nil {
		spans = append(spans, c.checkSpans("message spec check", id, check, append(found.Request, found.Response...), found.Skipped)...)
	}
	return finished{
		event: export.MessageEvent, cfg: c.cfg, chain: c.integration.Name, sc: c.trace.WithSpanID(id),
		at: seen.start, attrs: attrs, spans: spans,
	}
}

// closed keeps the session's first close message: its code and reason,
// when it has them.
func (r *reading) closed(fromApp bool, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shut != nil {
		return
	}
	r.shut = &closeMessage{fromApp: fromApp}
	if len(payload) >= 2 {
		r.shut.code = int(binary.BigEndian.Uint16(payload))
		r.shut.reason = string(payload[2:])
	}
}

// sessionAttrs is what the session record and span say about the
// messages, once both wires are read, which it waits for: how the session ended (Q7), the
// pings and pongs each way (Q24), and from which message on the messages
// could not be read (Q26).
func (r *reading) sessionAttrs() []attribute.KeyValue {
	<-r.app.done
	<-r.vendor.done
	r.mu.Lock()
	defer r.mu.Unlock()
	var attrs []attribute.KeyValue
	if s := r.shut; s != nil {
		attrs = append(attrs, attribute.String("hotline.websocket.close.from", side(s.fromApp)))
		if s.code != 0 {
			attrs = append(attrs, attribute.Int("hotline.websocket.close.code", s.code))
		}
		if s.reason != "" {
			attrs = append(attrs, attribute.String("hotline.websocket.close.reason", s.reason))
		}
	} else {
		attrs = append(attrs,
			attribute.String("hotline.websocket.close.from", "none"),
			attribute.String("hotline.websocket.closed_first", side(!r.vendorFirst.Load())))
	}
	attrs = append(attrs,
		attribute.Int64("hotline.websocket.from_app.pings", r.app.pings),
		attribute.Int64("hotline.websocket.from_app.pongs", r.app.pongs),
		attribute.Int64("hotline.websocket.from_vendor.pings", r.vendor.pings),
		attribute.Int64("hotline.websocket.from_vendor.pongs", r.vendor.pongs),
	)
	if r.unreadFrom > 0 {
		attrs = append(attrs, attribute.Int64("hotline.websocket.unread_from_index", r.unreadFrom))
	}
	return attrs
}

// side names a side of the session.
func side(app bool) string {
	if app {
		return "app"
	}
	return "vendor"
}

// vendorWire passes the vendor's bytes to its wire as they are read, and
// notes when the vendor's side ended before the proxy closed it.
type vendorWire struct {
	io.ReadCloser
	r *reading
}

func (v *vendorWire) Read(p []byte) (int, error) {
	n, err := v.ReadCloser.Read(p)
	v.r.vendor.pass(p[:n])
	if err != nil && !v.r.closing.Load() {
		v.r.vendorFirst.Store(true)
	}
	return n, err
}

func (v *vendorWire) Close() error {
	v.r.closing.Store(true)
	return v.ReadCloser.Close()
}

// appWire passes the app's bytes to its wire as they are written to the
// vendor.
type appWire struct {
	io.Writer
	r *reading
}

func (a appWire) Write(p []byte) (int, error) {
	n, err := a.Writer.Write(p)
	a.r.app.pass(p[:n])
	return n, err
}
