package proxy_test

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

// liveSocket is what the live spec's vendor does, and what both ends of
// the WebSocket sent.
type liveSocket struct {
	spec  string
	extra string
	// at is the place of the live integration in the config.
	at int
	// send are the messages the vendor sends once the WebSocket opens.
	send [][]byte
	// closeCode and closeReason, when set, are the close message the
	// vendor sends at once.
	closeCode   uint16
	closeReason string
	// deflate is the permessage-deflate the vendor's 101 agrees to, with
	// its parameters; empty for none.
	deflate string
	packer  *packer

	mu   sync.Mutex
	got  bytes.Buffer
	sent bytes.Buffer
}

func websocketMessageSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the live spec$`, func() error {
		w.live = &liveSocket{spec: liveSpec, at: len(w.integrations)}
		w.addIntegration("")
		w.useLiveSpec()
		w.vendor("api.shop.test").setAnswer(w.liveVendor)
		return nil
	})
	sc.Step(`^the live spec, where (.+)$`, func(change string) error {
		edits, known := liveSpecEdits()[change]
		if !known {
			return failf("no change %q to the live spec", change)
		}
		for _, edit := range edits {
			if !strings.Contains(w.live.spec, edit[0]) {
				return failf("the live spec has no %q to change", edit[0])
			}
			w.live.spec = strings.Replace(w.live.spec, edit[0], edit[1], 1)
		}
		if change == "toolCallAgain matches on toolCall alone as well" {
			w.live.spec += "    toolCallAgain:\n      type: object\n      properties:\n        toolCall: { type: object, x-hotline-match: true }\n"
		}
		w.useLiveSpec()
		return nil
	})
	sc.Step(`^the live spec, checking only messages$`, func() error {
		w.live.extra = "    checks: [messages]\n"
		w.useLiveSpec()
		return nil
	})
	sc.Step(`^the live spec, checking only headers and status$`, func() error {
		w.live.extra = "    checks: [headers, status]\n"
		w.useLiveSpec()
		return nil
	})
	sc.Step(`^the live spec with an overlay that adds reason to toolResponse's required fields$`, func() error {
		w.writeFile("specs/reason.overlay.yaml", `overlay: 1.1.0
info: { title: reason required, version: "1" }
actions:
  - target: $.components.schemas.toolResponse.properties.toolResponse.properties.functionResponses.items
    update: { required: [reason] }
`)
		w.live.extra = "    overlays: [./specs/reason.overlay.yaml]\n"
		w.useLiveSpec()
		return nil
	})
	sc.Step(`^the live vendor sends (.+)$`, func(messages string) error {
		for _, one := range strings.Split(messages, ", then ") {
			m, err := liveMessage(one)
			if err != nil {
				return err
			}
			w.live.send = append(w.live.send, m)
		}
		return nil
	})
	sc.Step(`^the live vendor closes with (\d+) "([^"]*)"$`, func(code int, reason string) error {
		w.live.closeCode, w.live.closeReason = uint16(code), reason //nolint:gosec // a close code the scenario writes
		return nil
	})
	sc.Step(`^the live vendor agrees to (.*permessage-deflate.*)$`, func(extensions string) error {
		w.live.deflate = extensions
		w.live.packer = &packer{alone: strings.Contains(extensions, "client_no_context_takeover")}
		return nil
	})
	sc.Step(`^the live vendor answers each ping with a pong$`, func() error {
		// The live vendor always does.
		return nil
	})
	sc.Step(`^an app opens the live WebSocket$`, func() error {
		var extra []string
		if w.live.deflate != "" {
			extra = append(extra, "Sec-WebSocket-Extensions: permessage-deflate")
		}
		return w.openSocketTo("forward_proxy", "api.shop.test", "/live", "talon-shop-a", extra...)
	})
	sc.Step(`^an app opens a WebSocket to the live spec's /other and sends "(\w+)"$`, func(text string) error {
		if err := w.openSocketTo("forward_proxy", "api.shop.test", "/other", "talon-shop-a"); err != nil {
			return err
		}
		if _, err := io.WriteString(w.socket.conn, text); err != nil {
			return err
		}
		return w.socket.conn.Close()
	})
	sc.Step(`^the app reads (a|\d+) messages?$`, func(count string) error {
		n, _ := strconv.Atoi(count)
		for range max(n, 1) {
			if op, _, err := readFrame(w.socket.reader); err != nil || op != 0x1 {
				return failf("the app read a frame %x, %v; want a text message", op, err)
			}
		}
		return nil
	})
	sc.Step(`^the app sends an empty message$`, func() error {
		return w.appWrite(frameOf(0x1, true, false, nil, true))
	})
	sc.Step(`^the app sends only (.+)$`, func(what string) error {
		first := frameOf(0x1, false, false, []byte(`{"a":`), true)
		frames := map[string][]byte{
			"a continuation frame that no message started":          frameOf(0x0, true, false, []byte(`{}`), true),
			"a frame marked compressed, with no compression agreed": frameOf(0x1, true, true, []byte(`{}`), true),
			"a ping of 126 bytes":                                   frameOf(0x9, true, false, bytes.Repeat([]byte("p"), 126), true),
			"a ping in 2 frames":                                    frameOf(0x9, false, false, []byte("p"), true),
			"a text frame inside a message in frames":               append(first, frameOf(0x1, true, false, []byte(`1}`), true)...),
			"a frame longer than any length":                        {0x81, 0xff, 0x80, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4},
			"1 byte of a frame header":                              {0x81},
			"3 bytes of a masked frame header":                      {0x81, 0x85, 0x11},
			"a ping header without its payload":                     frameOf(0x9, true, false, []byte("ping"), true)[:6],
			"the first frame of a message in frames":                first,
			"half a frame's payload":                                frameOf(0x1, true, false, toolResponse("call-7"), true)[:40],
			"a packed message that can't be unpacked":               frameOf(0x1, true, true, []byte{0xff, 0xff, 0xff, 0xff}, true),
		}[what]
		if frames == nil {
			return failf("no frames %q", what)
		}
		return w.appWrite(frames)
	})
	sc.Step(`^the app sends ((?:\d+ (?:pings|bytes|toolResponse)|a (?:packed )?toolResponse|toolResponse |\{).+)$`, w.appSends)
	sc.Step(`^the app closes the live WebSocket$`, func() error {
		if err := w.appWrite(frameOf(0x8, true, false, []byte{0x03, 0xe8}, true)); err != nil {
			return err
		}
		return w.untilClose()
	})
	sc.Step(`^the app waits for the vendor to close$`, w.untilClose)
	sc.Step(`^the app drops its connection without a close message$`, func() error {
		return w.socket.conn.Close()
	})
	sc.Step(`^there are (\d+) message records$`, func(n int) error {
		if got := w.messageRecords(); len(got) != n {
			return failf("there are %d message records, want %d", len(got), n)
		}
		return nil
	})
	sc.Step(`^there are (\d+) message records, each (.+)$`, func(n int, pairs string) error {
		got := w.messageRecords()
		if len(got) != n {
			return failf("there are %d message records, want %d", len(got), n)
		}
		var errs []error
		for i, r := range got {
			if err := says(r.attrs, pairs); err != nil {
				errs = append(errs, fmt.Errorf("message record %d: %w", i+1, err))
			}
		}
		return errors.Join(errs...)
	})
	sc.Step(`^message record (\d+) says (.+)$`, func(n int, pairs string) error {
		r, err := w.messageRecord(n)
		if err != nil {
			return err
		}
		return says(r.attrs, pairs)
	})
	sc.Step(`^message record (\d+) and its span say (.+)$`, func(n int, pairs string) error {
		r, err := w.messageRecord(n)
		if err != nil {
			return err
		}
		spans := w.receivers[0].spans()
		i := slices.IndexFunc(spans, func(s receivedSpan) bool { return s.spanID == r.spanID })
		if i < 0 {
			return failf("no span has message record %d's span id", n)
		}
		return errors.Join(says(r.attrs, pairs), says(spans[i].attrs, pairs))
	})
	sc.Step(`^message records (\d+) to (\d+) say (.+)$`, func(from, to int, pairs string) error {
		var errs []error
		for n := from; n <= to; n++ {
			r, err := w.messageRecord(n)
			if err == nil {
				err = says(r.attrs, pairs)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("message record %d: %w", n, err))
			}
		}
		return errors.Join(errs...)
	})
	sc.Step(`^message record (\d+) has no (.+)$`, func(n int, keys string) error {
		r, err := w.messageRecord(n)
		if err != nil {
			return err
		}
		return hasNo(r.attrs, regexp.MustCompile(`, | or `).Split(keys, -1)...)
	})
	sc.Step(`^message record (\d+)'s request results are (.+)$`, func(n int, want string) error {
		r, err := w.messageRecord(n)
		if err != nil {
			return err
		}
		got, err := resultsOf(r.attrs, "request")
		if err != nil {
			return err
		}
		if strings.Join(got, "; ") != want {
			return failf("the results are %q, want %q", strings.Join(got, "; "), want)
		}
		return nil
	})
	sc.Step(`^the live vendor got all (\d+) bytes of the message unchanged$`, func(size int) error {
		r, err := w.messageRecord(1)
		if err != nil {
			return err
		}
		return errors.Join(has(r.attrs, "hotline.websocket.message.size", size), w.live.unchanged())
	})
	sc.Step(`^the live vendor got every byte as the app sent it$`, func() error {
		w.settle()
		return w.live.unchanged()
	})
	sc.Step(`^each message record's span is a "websocket message" span under the "websocket session" span$`, func() error {
		messages := w.messageRecords()
		sessions := w.receivers[0].sessionRecords()
		if len(sessions) != 1 || len(messages) == 0 {
			return failf("there are %d session records and %d message records", len(sessions), len(messages))
		}
		spans := w.receivers[0].spans()
		for _, m := range messages {
			i := slices.IndexFunc(spans, func(s receivedSpan) bool { return s.spanID == m.spanID })
			switch {
			case i < 0:
				return failf("no span has the message record's span id %s", m.spanID)
			case spans[i].name != "websocket message" || spans[i].parent != sessions[0].spanID || spans[i].traceID != m.traceID:
				return failf("the message's span is %q under %s, want websocket message under %s", spans[i].name, spans[i].parent, sessions[0].spanID)
			}
		}
		return nil
	})
	sc.Step(`^each "websocket message" span has a "message spec check" span under it, with a "wait for check worker" span under that$`, func() error {
		w.settle()
		spans := w.receivers[0].spans()
		under := func(parent, name string) (receivedSpan, bool) {
			i := slices.IndexFunc(spans, func(s receivedSpan) bool { return s.parent == parent && s.name == name })
			if i < 0 {
				return receivedSpan{}, false
			}
			return spans[i], true
		}
		for _, s := range spans {
			if s.name != "websocket message" {
				continue
			}
			check, found := under(s.spanID, "message spec check")
			if !found {
				return failf("the message span %s has no message spec check under it", s.spanID)
			}
			if _, found := under(check.spanID, "wait for check worker"); !found {
				return failf("the message spec check %s has no wait for check worker under it", check.spanID)
			}
		}
		return nil
	})
	sc.Step(`^a message record and a "websocket message" span arrive while the WebSocket is open$`, func() error {
		defer func() { _ = w.socket.conn.Close() }()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			spans := w.receivers[0].spans()
			if len(w.receivers[0].messageRecords()) > 0 && slices.ContainsFunc(spans, func(s receivedSpan) bool { return s.name == "websocket message" }) {
				if sessions := w.receivers[0].sessionRecords(); len(sessions) > 0 {
					return failf("the session ended before its message was sent")
				}
				return nil
			}
		}
		return failf("no message record and span arrived while the WebSocket was open")
	})
	sc.Step(`^the session record and the websocket session span say (.+)$`, func(pairs string) error {
		_, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		spans := w.receivers[0].spans()
		i := slices.IndexFunc(spans, func(s receivedSpan) bool { return s.spanID == session.spanID })
		if i < 0 {
			return failf("no span has the session record's span id")
		}
		return errors.Join(says(session.attrs, pairs), says(spans[i].attrs, pairs))
	})
	sc.Step(`^the session record has no (\S+)$`, func(key string) error {
		_, session, err := w.callAndSession()
		if err != nil {
			return err
		}
		return hasNo(session.attrs, key)
	})
}

// liveSpecEdits are the changes a scenario can make to the live spec,
// each a list of texts to replace once.
func liveSpecEdits() map[string][][2]string {
	const (
		toolCallLine      = `            messages: [{ $ref: "#/components/x-async-api/messages/toolCall" }]`
		sessionInfoLine   = `            messages: [{ $ref: "#/components/x-async-api/messages/sessionInfo" }]`
		serverContentLine = `            messages: [{ $ref: "#/components/x-async-api/messages/serverContent" }]`
		responsePayload   = `        payload: { $ref: "#/components/schemas/toolResponse" }`
		sessionPayload    = `        payload: { $ref: "#/components/schemas/sessionInfo" }`
		toolCallLocation  = `"$message.payload#/toolCall/functionCalls/0/id"`
	)
	return map[string][][2]string{
		`sessionInfo also marks session.kind with const "text"`: {{"kind: { type: string }", "kind: { type: string, x-hotline-match: true, const: text }"}},
		"toolCall has no x-hotline-match":                       {{"          x-hotline-match: true # toolCall\n", ""}},
		"an x-hotline-match sits inside functionCalls":          {{"name: { type: string } # toolCall name", "name: { type: string, x-hotline-match: true }"}},
		"toolCallAgain matches on toolCall alone as well": {
			{serverContentLine, strings.TrimSuffix(serverContentLine, "]") + `, { $ref: "#/components/x-async-api/messages/toolCallAgain" }]`},
			{"  x-async-api:\n    messages:\n", "  x-async-api:\n    messages:\n      toolCallAgain: { payload: { $ref: \"#/components/schemas/toolCallAgain\" } }\n"},
		},
		"toolCall's x-hotline-match is yes":             {{"x-hotline-match: true # toolCall", "x-hotline-match: yes # toolCall"}},
		"a field of toolResponse points nowhere":        {{"response: { type: object }", `response: { $ref: "#/components/schemas/nothing" }`}},
		"toolCall is not JSON Schema":                   {{"required: [toolCall]", "required: 5"}},
		"x-async-api is a list":                         {{"      x-async-api:\n        operations:\n", "      x-async-api: [nothing]\n      x-unused:\n        operations:\n"}},
		"x-async-api also holds channels":               {{"        operations:\n", "        channels: {}\n        operations:\n"}},
		"toolCall's action is publish":                  {{"            action: send\n", "            action: publish\n"}},
		"toolCall's messages is 5":                      {{toolCallLine, "            messages: 5"}},
		"sessionInfo lists a message written in place":  {{sessionInfoLine, "            messages: [{ payload: { type: object } }]"}},
		"sessionInfo lists a message that is not there": {{sessionInfoLine, `            messages: [{ $ref: "#/components/x-async-api/messages/nothing" }]`}},
		"sessionInfo's payload is written in place":     {{sessionPayload, "        payload: { type: object }"}},
		"sessionInfo's payload points nowhere":          {{sessionPayload, `        payload: { $ref: "#/components/schemas/nothing" }`}},
		"the vendor's messages mix JSON and audio/pcm":  {{`        payload: { $ref: "#/components/schemas/toolCall" }`, `        payload: { $ref: "#/components/schemas/toolCall" }` + "\n        contentType: audio/pcm"}},
		"sessionInfo lists 5":                           {{sessionInfoLine, "            messages: [5]"}},
		"sessionInfo has no payload":                    {{sessionPayload, "        summary: no payload"}},
		"serverContent also lists toolCall": {
			{serverContentLine, strings.TrimSuffix(serverContentLine, "]") + `, { $ref: "#/components/x-async-api/messages/toolCall" }]`},
		},
		"toolCall's reply names a channel":          {{"            reply:\n", "            reply:\n              channel: { $ref: \"#/channels/live\" }\n"}},
		"toolCall's correlationId is in a header":   {{toolCallLocation, `"$message.header#/id"`}},
		"toolCall's correlationId is not a pointer": {{toolCallLocation, `"toolCall.functionCalls.0.id"`}},
		"the app's messages mix JSON and audio/pcm": {{responsePayload, responsePayload + "\n        contentType: audio/pcm"}},
		"the app's messages are audio/pcm": {
			{responsePayload, responsePayload + "\n        contentType: audio/pcm"},
			{sessionPayload, sessionPayload + "\n        contentType: audio/pcm"},
		},
		"the route still uses x-hotline-messages":  {{"      x-async-api:\n", "      x-hotline-messages: { fromApp: [] }\n      x-async-api:\n"}},
		"toolCall marks its id x-hotline-id-tuple": {{"id: { type: string } # toolCall id", "id: { type: string, x-hotline-id-tuple: tool-call }"}},
	}
}

// useLiveSpec writes the live spec and puts its integration in its place.
func (w *world) useLiveSpec() {
	w.integrations[w.live.at] = integration("talon-shop-a", w.specAPI("api.shop.test", w.live.spec, w.live.extra))
}

// liveVendor is the live spec's vendor: it switches to a WebSocket, sends
// its messages, answers pings and close messages, and keeps every byte the
// app sent.
func (w *world) liveVendor(rw http.ResponseWriter, r *http.Request) {
	l := w.live
	rw.Header().Set("Connection", "Upgrade")
	rw.Header().Set("Upgrade", "websocket")
	if l.deflate != "" {
		rw.Header().Set("Sec-WebSocket-Extensions", l.deflate)
	}
	rw.WriteHeader(http.StatusSwitchingProtocols)
	conn, buf, err := http.NewResponseController(rw).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if r.URL.Path != "/live" {
		_, _ = io.Copy(io.Discard, buf)
		return
	}
	for _, m := range l.send {
		_, _ = conn.Write(frameOf(0x1, true, false, m, false))
	}
	if l.closeCode != 0 {
		payload := binary.BigEndian.AppendUint16(nil, l.closeCode)
		_, _ = conn.Write(frameOf(0x8, true, false, append(payload, l.closeReason...), false))
	}
	in := bufio.NewReader(io.TeeReader(buf, writerFunc(func(p []byte) (int, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.got.Write(p)
	})))
	for {
		op, payload, err := readFrame(in)
		switch {
		case err != nil:
			return
		case op == 0x9:
			_, _ = conn.Write(frameOf(0xa, true, false, payload, false))
		case op == 0x8:
			if l.closeCode == 0 {
				_, _ = conn.Write(frameOf(0x8, true, false, payload, false))
			}
			return
		}
	}
}

// writerFunc is a function used as an io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// unchanged says whether the vendor got exactly what the app sent.
func (l *liveSocket) unchanged() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !bytes.Equal(l.got.Bytes(), l.sent.Bytes()) {
		return failf("the vendor got %d bytes, the app sent %d, and they differ", l.got.Len(), l.sent.Len())
	}
	return nil
}

// appWrite sends bytes from the app, and keeps them.
func (w *world) appWrite(b []byte) error {
	w.live.mu.Lock()
	w.live.sent.Write(b)
	w.live.mu.Unlock()
	_, err := w.socket.conn.Write(b)
	return err
}

// untilClose reads until the vendor's close message, or the end, and
// closes the app's side.
func (w *world) untilClose() error {
	defer func() { _ = w.socket.conn.Close() }()
	for {
		op, _, err := readFrame(w.socket.reader)
		if err != nil || op == 0x8 {
			return nil
		}
	}
}

// appSends sends what the app sends: messages, pings, or large messages,
// joined with ", then ".
func (w *world) appSends(what string) error {
	pings := regexp.MustCompile(`^(\d+) pings and reads (\d+) pongs$`)
	audio := regexp.MustCompile(`^(\d+) bytes of audio as a binary frame$`)
	large := regexp.MustCompile(`^a (packed )?toolResponse (?:of|that unpacks to) (\d+) MiB(?: with id "([^"]+)")?$`)
	packed := regexp.MustCompile(`^(\d+) toolResponse messages, packed$`)
	plain := regexp.MustCompile(`^(.+?)(?: as a (binary) frame| split into (\d+) frames, with a ping after the first)?$`)
	for _, part := range strings.Split(what, ", then ") {
		var err error
		switch {
		case pings.MatchString(part):
			n, _ := strconv.Atoi(pings.FindStringSubmatch(part)[1])
			for range n {
				if err = w.appWrite(frameOf(0x9, true, false, []byte("hi"), true)); err != nil {
					return err
				}
			}
			for range n {
				if op, _, err := readFrame(w.socket.reader); err != nil || op != 0xa {
					return failf("the app read a frame %x, %v; want a pong", op, err)
				}
			}
		case audio.MatchString(part):
			n, _ := strconv.Atoi(audio.FindStringSubmatch(part)[1])
			err = w.appWrite(frameOf(0x2, true, false, bytes.Repeat([]byte{0x7f}, n), true))
		case large.MatchString(part):
			m := large.FindStringSubmatch(part)
			mib, _ := strconv.Atoi(m[2])
			err = w.appMessage(bigToolResponse(mib<<20, m[3]), m[1] != "")
		case packed.MatchString(part):
			n, _ := strconv.Atoi(packed.FindStringSubmatch(part)[1])
			for i := range n {
				if err = w.appMessage(toolResponse(fmt.Sprintf("call-%d", i+1)), true); err != nil {
					return err
				}
			}
		default:
			m := plain.FindStringSubmatch(part)
			message, err := liveMessage(m[1])
			if err != nil {
				return err
			}
			switch {
			case m[2] == "binary":
				err = w.appWrite(frameOf(0x2, true, false, message, true))
			case m[3] != "":
				n, _ := strconv.Atoi(m[3])
				err = w.appWrite(split(message, n))
			default:
				err = w.appWrite(frameOf(0x1, true, false, message, true))
			}
			if err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// appMessage sends one text message from the app, packed when asked.
func (w *world) appMessage(message []byte, pack bool) error {
	if !pack {
		return w.appWrite(frameOf(0x1, true, false, message, true))
	}
	return w.appWrite(frameOf(0x1, true, true, w.live.packer.pack(message), true))
}

// messageRecords are the message records, by their index, once the proxy
// has sent everything.
func (w *world) messageRecords() []receivedRecord {
	w.settle()
	records := w.receivers[0].messageRecords()
	slices.SortFunc(records, func(a, b receivedRecord) int {
		return strings.Compare(fmt.Sprintf("%09v", a.attrs["hotline.websocket.message.index"]), fmt.Sprintf("%09v", b.attrs["hotline.websocket.message.index"]))
	})
	return records
}

// messageRecord is the message record with index n.
func (w *world) messageRecord(n int) (receivedRecord, error) {
	records := w.messageRecords()
	if n < 1 || n > len(records) {
		return receivedRecord{}, failf("there are %d message records, want a record %d", len(records), n)
	}
	r := records[n-1]
	return r, has(r.attrs, "hotline.websocket.message.index", n)
}

// says checks the pairs "key value, key value" against a record's
// attributes.
func says(attrs map[string]any, pairs string) error {
	var errs []error
	for _, pair := range strings.Split(pairs, ", ") {
		key, value, _ := strings.Cut(pair, " ")
		errs = append(errs, has(attrs, key, value))
	}
	return errors.Join(errs...)
}

// liveMessage is the text of a message written in a scenario: JSON as it
// is, or toolCall, toolResponse, toolError or serverContent with their ids
// or counts.
func liveMessage(text string) ([]byte, error) {
	ids := regexp.MustCompile(`"([^"]+)"`)
	var found []string
	for _, m := range ids.FindAllStringSubmatch(text, -1) {
		found = append(found, m[1])
	}
	switch {
	case strings.HasPrefix(text, "{"):
		return []byte(text), nil
	case strings.HasPrefix(text, "toolCall with id"):
		var calls []string
		for _, id := range found {
			calls = append(calls, `{"id":"`+id+`","name":"orderStatus"}`)
		}
		return []byte(`{"toolCall":{"functionCalls":[` + strings.Join(calls, ",") + `]}}`), nil
	case strings.HasPrefix(text, "toolError with id"):
		return []byte(`{"toolError":{"id":"` + found[0] + `","message":"no such order"}}`), nil
	case strings.HasPrefix(text, "toolResponse with id"):
		return toolResponse(found[0]), nil
	case strings.HasPrefix(text, "serverContent with totalTokenCount "):
		count := strings.TrimPrefix(text, "serverContent with totalTokenCount ")
		return []byte(`{"serverContent":{"turnComplete":true},"usageMetadata":{"totalTokenCount":` + count + `}}`), nil
	}
	return nil, failf("no message %q", text)
}

func toolResponse(id string) []byte {
	return []byte(`{"toolResponse":{"functionResponses":[{"id":"` + id + `","name":"orderStatus","response":{"status":"sent"}}]}}`)
}

// bigToolResponse is a toolResponse of size bytes, its id first, then a
// list and an object that close before its long text.
func bigToolResponse(size int, id string) []byte {
	if id == "" {
		id = "call-1"
	}
	head := `{"toolResponse":{"functionResponses":[{"id":"` + id + `","name":"orderStatus","tags":["a"],"meta":{},"response":{"text":"`
	tail := `"}}]}}`
	return []byte(head + strings.Repeat("a", size-len(head)-len(tail)) + tail)
}

// frameOf is one WebSocket frame; an app's frames are masked.
func frameOf(op byte, fin, rsv1 bool, payload []byte, masked bool) []byte {
	first := op
	if fin {
		first |= 0x80
	}
	if rsv1 {
		first |= 0x40
	}
	out := []byte{first}
	var maskBit byte
	if masked {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		out = append(out, maskBit|byte(n))
	case n < 1<<16:
		out = binary.BigEndian.AppendUint16(append(out, maskBit|126), uint16(n))
	default:
		out = binary.BigEndian.AppendUint64(append(out, maskBit|127), uint64(n))
	}
	if !masked {
		return append(out, payload...)
	}
	key := [4]byte{0x11, 0x22, 0x33, 0x44}
	out = append(out, key[:]...)
	for i, b := range payload {
		out = append(out, b^key[i%4])
	}
	return out
}

// split is a text message from the app in n frames, with a ping after
// the first.
func split(message []byte, n int) []byte {
	var out []byte
	size := (len(message) + n - 1) / n
	for i := range n {
		part := message[min(i*size, len(message)):min((i+1)*size, len(message))]
		op := byte(0x0)
		if i == 0 {
			op = 0x1
		}
		out = append(out, frameOf(op, i == n-1, false, part, true)...)
		if i == 0 {
			out = append(out, frameOf(0x9, true, false, []byte("hi"), true)...)
		}
	}
	return out
}

// readFrame reads one frame, unmasked, with its opcode; a message in
// several frames is read as its frames.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > 64<<20 {
		return 0, nil, failf("a frame of %d bytes is longer than the tests send", n)
	}
	var key [4]byte
	masked := head[1]&0x80 != 0
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= key[i%4]
		}
	}
	return head[0] & 0x0f, payload, nil
}

// packer packs the app's messages with permessage-deflate: against the
// earlier ones, or each alone.
type packer struct {
	alone bool
	w     *flate.Writer
	buf   bytes.Buffer
}

func (p *packer) pack(message []byte) []byte {
	p.buf.Reset()
	if p.w == nil || p.alone {
		p.w, _ = flate.NewWriter(&p.buf, flate.BestSpeed)
	}
	_, _ = p.w.Write(message)
	_ = p.w.Flush()
	return bytes.Clone(bytes.TrimSuffix(p.buf.Bytes(), []byte{0x00, 0x00, 0xff, 0xff}))
}

// liveSpec is a shop api whose GET /live switches to a WebSocket and lists
// its operations and messages; GET /other switches too, with none listed.
const liveSpec = `openapi: 3.1.0
info: { title: shop live, version: "1" }
paths:
  /live:
    get:
      responses:
        "101": { description: switched to a WebSocket }
      x-async-api:
        operations:
          toolCall:
            action: send
            messages: [{ $ref: "#/components/x-async-api/messages/toolCall" }]
            reply:
              messages: [{ $ref: "#/components/x-async-api/messages/toolResponse" }]
          toolError:
            action: send
            messages: [{ $ref: "#/components/x-async-api/messages/toolError" }]
          sessionInfo:
            action: receive
            messages: [{ $ref: "#/components/x-async-api/messages/sessionInfo" }]
          serverContent:
            action: send
            messages: [{ $ref: "#/components/x-async-api/messages/serverContent" }]
  /other:
    get:
      responses:
        "101": { description: switched to a WebSocket }
components:
  x-async-api:
    messages:
      toolCall:
        payload: { $ref: "#/components/schemas/toolCall" }
        correlationId: { location: "$message.payload#/toolCall/functionCalls/0/id" }
      toolResponse:
        payload: { $ref: "#/components/schemas/toolResponse" }
        correlationId: { location: "$message.payload#/toolResponse/functionResponses/0/id" }
      toolError:
        payload: { $ref: "#/components/schemas/toolError" }
        correlationId: { location: "$message.payload#/toolError/id" }
      sessionInfo:
        payload: { $ref: "#/components/schemas/sessionInfo" }
      serverContent:
        payload: { $ref: "#/components/schemas/serverContent" }
  schemas:
    toolCall:
      type: object
      required: [toolCall]
      properties:
        toolCall:
          x-hotline-match: true # toolCall
          type: object
          required: [functionCalls]
          properties:
            functionCalls:
              type: array
              items:
                type: object
                required: [id, name]
                properties:
                  id: { type: string } # toolCall id
                  name: { type: string } # toolCall name
    toolResponse:
      type: object
      required: [toolResponse]
      properties:
        toolResponse:
          x-hotline-match: true
          type: object
          required: [functionResponses]
          properties:
            functionResponses:
              type: array
              items:
                type: object
                required: [id, name, response]
                properties:
                  id: {}
                  name: { type: string }
                  response: { type: object }
    toolError:
      type: object
      required: [toolError]
      properties:
        toolError:
          x-hotline-match: true
          type: object
          required: [id]
          properties:
            id: { type: string }
            message: { type: string }
    sessionInfo:
      type: object
      required: [type, session]
      properties:
        type: { x-hotline-match: true, const: session.info }
        session:
          type: object
          properties:
            kind: { type: string }
            parent: { $ref: "#/components/schemas/sessionInfo" }
    serverContent:
      type: object
      properties:
        serverContent: { x-hotline-match: true, type: object }
        usageMetadata:
          type: object
          properties:
            totalTokenCount: { type: integer, x-hotline-record: gemini.tokens.total }
`
