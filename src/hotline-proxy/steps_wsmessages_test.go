package proxy_test

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
	"go.yaml.in/yaml/v3"
)

// scriptedVendor is the WebSocket vendor of the message scenarios, the fake
// Kraken or the tool spec's vendor: what its spec is, what it does, and what
// both ends of the WebSocket sent.
type scriptedVendor struct {
	spec  string
	extra string
	// id, host and path are the integration, the vendor's host and the
	// address the app opens.
	id, host, path string
	// at is the place of the integration in the config.
	at int
	// send are the messages the vendor sends once the WebSocket opens;
	// answer are the ones it sends once the app's first message comes.
	send, answer [][]byte
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

// krakenSpec is the shared Kraken spec, as the repository holds it.
const krakenSpec = "../../specs/kraken/spot-websocket/v2.openapi.yaml"

func websocketMessageSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the Kraken spec$`, func() error {
		spec, err := os.ReadFile(filepath.FromSlash(krakenSpec))
		if err != nil {
			return err
		}
		w.useScripted(&scriptedVendor{spec: string(spec), id: "trading-bot", host: "ws-auth.kraken.test", path: "/v2"})
		return nil
	})
	sc.Step(`^the tool spec$`, func() error {
		w.useScripted(&scriptedVendor{spec: toolSpec, id: "talon-shop-a", host: "api.shop.test", path: "/live"})
		return nil
	})
	sc.Step(`^the Kraken spec, where (.+)$`, func(change string) error {
		edits, known := krakenSpecEdits()[change]
		if !known {
			return failf("no change %q to the Kraken spec", change)
		}
		spec, err := editSpec(w.scripted.spec, edits)
		if err != nil {
			return err
		}
		w.scripted.spec = spec
		w.writeScriptedSpec()
		return nil
	})
	sc.Step(`^the Kraken spec, checking only messages$`, func() error {
		w.scripted.extra = "    checks: [messages]\n"
		w.writeScriptedSpec()
		return nil
	})
	sc.Step(`^the Kraken spec, checking only headers and status$`, func() error {
		w.scripted.extra = "    checks: [headers, status]\n"
		w.writeScriptedSpec()
		return nil
	})
	sc.Step(`^the Kraken spec with an overlay that adds cl_ord_id to add_order's required params$`, func() error {
		w.writeFile("specs/cl-ord-id.overlay.yaml", `overlay: 1.1.0
info: { title: cl_ord_id required, version: "1" }
actions:
  - target: $.components.schemas.addOrder.properties.params
    update: { required: [cl_ord_id] }
`)
		w.scripted.extra = "    overlays: [./specs/cl-ord-id.overlay.yaml]\n"
		w.writeScriptedSpec()
		return nil
	})
	sc.Step(`^the (?:Kraken|tool) vendor sends (.+)$`, func(messages string) error {
		return w.scripted.script(&w.scripted.send, messages)
	})
	sc.Step(`^the Kraken vendor answers the app's first message with (.+)$`, func(messages string) error {
		return w.scripted.script(&w.scripted.answer, messages)
	})
	sc.Step(`^the Kraken vendor closes with (\d+) "([^"]*)"$`, func(code int, reason string) error {
		w.scripted.closeCode, w.scripted.closeReason = uint16(code), reason //nolint:gosec // a close code the scenario writes
		return nil
	})
	sc.Step(`^the Kraken vendor agrees to (.*permessage-deflate.*)$`, func(extensions string) error {
		w.scripted.deflate = extensions
		w.scripted.packer = &packer{alone: strings.Contains(extensions, "client_no_context_takeover")}
		return nil
	})
	sc.Step(`^the Kraken vendor answers each ping with a pong$`, func() error {
		// The scripted vendor always does.
		return nil
	})
	sc.Step(`^an app opens the (?:Kraken|tool) WebSocket$`, func() error {
		var extra []string
		if w.scripted.deflate != "" {
			extra = append(extra, "Sec-WebSocket-Extensions: permessage-deflate")
		}
		return w.openSocketTo("forward_proxy", w.scripted.host, w.scripted.path, w.scripted.id, extra...)
	})
	sc.Step(`^an app opens a WebSocket to the tool spec's /other and sends "(\w+)"$`, func(text string) error {
		if err := w.openSocketTo("forward_proxy", w.scripted.host, "/other", w.scripted.id); err != nil {
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
			"half a frame's payload":                                frameOf(0x1, true, false, addOrder(7), true)[:40],
			"a packed message that can't be unpacked":               frameOf(0x1, true, true, []byte{0xff, 0xff, 0xff, 0xff}, true),
		}[what]
		if frames == nil {
			return failf("no frames %q", what)
		}
		return w.appWrite(frames)
	})
	sc.Step(`^the app sends ((?:\d+ (?:pings|bytes|add_order)|a (?:packed )?batch_add|a toolResponse|add_order |toolResponse |\{).+)$`, w.appSends)
	sc.Step(`^the app closes the (?:Kraken|tool) WebSocket$`, func() error {
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
	sc.Step(`^the Kraken vendor got all (\d+) bytes of the message unchanged$`, func(size int) error {
		r, err := w.messageRecord(1)
		if err != nil {
			return err
		}
		return errors.Join(has(r.attrs, "hotline.websocket.message.size", size), w.scripted.unchanged())
	})
	sc.Step(`^the Kraken vendor got every byte as the app sent it$`, func() error {
		w.settle()
		return w.scripted.unchanged()
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

// specEdit sets the value at a JSON pointer in a spec, as YAML; an empty
// value deletes it. A list index one past the end adds to the list.
type specEdit struct{ at, to string }

// krakenSpecEdits are the changes a scenario can make to its copy of the
// Kraken spec; the shared file never changes.
func krakenSpecEdits() map[string][]specEdit {
	const (
		route      = "/paths/~1v2/get"
		operations = route + "/x-async-api/operations/"
		messages   = "/components/x-async-api/messages/"
		schemas    = "/components/schemas/"
	)
	return map[string][]specEdit{
		"ping has no x-hotline-match":                       {{schemas + "ping/properties/method/x-hotline-match", ""}},
		"an x-hotline-match sits inside batch_add's orders": {{schemas + "batchAdd/properties/params/properties/orders/items", "{type: object, properties: {side: {type: string, x-hotline-match: true}}}"}},
		"pingAgain matches on method ping as well": {
			{operations + "ping/messages/1", `{$ref: "#/components/x-async-api/messages/pingAgain"}`},
			{messages + "pingAgain", `{payload: {$ref: "#/components/schemas/pingAgain"}}`},
			{schemas + "pingAgain", "{type: object, properties: {method: {const: ping, x-hotline-match: true}}}"},
		},
		"ping's x-hotline-match is yes":                {{schemas + "ping/properties/method/x-hotline-match", "yes"}},
		"a field of add_order points nowhere":          {{schemas + "addOrder/properties/params/properties/side", `{$ref: "#/components/schemas/nothing"}`}},
		"add_order is not JSON Schema":                 {{schemas + "addOrder/required", "5"}},
		"x-async-api is a list":                        {{route + "/x-async-api", "[nothing]"}},
		"x-async-api also holds channels":              {{route + "/x-async-api/channels", "{}"}},
		"ping's action is publish":                     {{operations + "ping/action", "publish"}},
		"ping's messages is 5":                         {{operations + "ping/messages", "5"}},
		"heartbeat lists a message written in place":   {{operations + "heartbeat/messages/0", "{payload: {type: object}}"}},
		"heartbeat lists 5":                            {{operations + "heartbeat/messages/0", "5"}},
		"heartbeat has no payload":                     {{messages + "heartbeat", "{summary: no payload}"}},
		"heartbeat's payload points nowhere":           {{messages + "heartbeat/payload", `{$ref: "#/components/schemas/nothing"}`}},
		"heartbeat lists a message that is not there":  {{operations + "heartbeat/messages/0", `{$ref: "#/components/x-async-api/messages/nothing"}`}},
		"heartbeat's payload is written in place":      {{messages + "heartbeat/payload", "{type: object}"}},
		"status also lists heartbeat":                  {{operations + "status/messages/1", `{$ref: "#/components/x-async-api/messages/heartbeat"}`}},
		"ping's reply names a channel":                 {{operations + "ping/reply/channel", `{$ref: "#/channels/v2"}`}},
		"ping's correlationId is in a header":          {{messages + "ping/correlationId", `{location: "$message.header#/req_id"}`}},
		"ping's correlationId is not a pointer":        {{messages + "ping/correlationId", "{location: req_id}"}},
		"the app's messages mix JSON and audio/pcm":    {{messages + "ping/contentType", "audio/pcm"}},
		"the vendor's messages mix JSON and audio/pcm": {{messages + "heartbeat/contentType", "audio/pcm"}},
		"every message is audio/pcm":                   {{route + "/x-async-api/defaultContentType", "audio/pcm"}},
		"the route still uses x-hotline-messages":      {{route + "/x-hotline-messages", "{fromApp: []}"}},
		"ping marks its req_id x-hotline-id-tuple":     {{schemas + "ping/properties/req_id/x-hotline-id-tuple", "ping"}},
	}
}

// editSpec makes the edits to a spec written in YAML.
func editSpec(spec string, edits []specEdit) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(spec), &doc); err != nil {
		return "", err
	}
	for _, e := range edits {
		var to *yaml.Node
		if e.to != "" {
			var value yaml.Node
			if err := yaml.Unmarshal([]byte(e.to), &value); err != nil {
				return "", err
			}
			to = value.Content[0]
		}
		if err := setAt(doc.Content[0], strings.Split(e.at, "/")[1:], to); err != nil {
			return "", fmt.Errorf("%s: %w", e.at, err)
		}
	}
	out, err := yaml.Marshal(&doc)
	return string(out), err
}

// setAt puts value at the path under node, or deletes what is there when
// value is nil. Path parts are escaped as in a JSON pointer.
func setAt(node *yaml.Node, path []string, value *yaml.Node) error {
	key := strings.NewReplacer("~1", "/", "~0", "~").Replace(path[0])
	last := len(path) == 1
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value != key {
				continue
			}
			switch {
			case !last:
				return setAt(node.Content[i+1], path[1:], value)
			case value == nil:
				node.Content = slices.Delete(node.Content, i, i+2)
			default:
				node.Content[i+1] = value
			}
			return nil
		}
		if !last || value == nil {
			return failf("the spec has no %q", key)
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
		return nil
	case yaml.SequenceNode:
		i, err := strconv.Atoi(key)
		switch {
		case err != nil || i < 0 || i > len(node.Content) || (i == len(node.Content) && (!last || value == nil)):
			return failf("the list has no item %q", key)
		case !last:
			return setAt(node.Content[i], path[1:], value)
		case value == nil:
			node.Content = slices.Delete(node.Content, i, i+1)
		case i == len(node.Content):
			node.Content = append(node.Content, value)
		default:
			node.Content[i] = value
		}
		return nil
	}
	return failf("%q is inside a value that holds nothing", key)
}

// useScripted makes v the scenario's WebSocket vendor, with its
// integration and spec.
func (w *world) useScripted(v *scriptedVendor) {
	v.at = len(w.integrations)
	w.scripted = v
	w.addIntegration("")
	w.writeScriptedSpec()
	w.vendor(v.host).setAnswer(w.scriptedVendor)
}

// writeScriptedSpec writes the vendor's spec and puts its integration in
// its place.
func (w *world) writeScriptedSpec() {
	v := w.scripted
	w.integrations[v.at] = integration(v.id, w.specAPI(v.host, v.spec, v.extra))
}

// script adds the messages, joined with ", then ", to a list of what the
// vendor sends.
func (v *scriptedVendor) script(list *[][]byte, messages string) error {
	for _, one := range strings.Split(messages, ", then ") {
		m, err := scriptedMessage(one)
		if err != nil {
			return err
		}
		*list = append(*list, m)
	}
	return nil
}

// scriptedVendor switches to a WebSocket, sends its messages, answers the
// app's first message, pings and close messages, and keeps every byte the
// app sent.
func (w *world) scriptedVendor(rw http.ResponseWriter, r *http.Request) {
	v := w.scripted
	rw.Header().Set("Connection", "Upgrade")
	rw.Header().Set("Upgrade", "websocket")
	if v.deflate != "" {
		rw.Header().Set("Sec-WebSocket-Extensions", v.deflate)
	}
	rw.WriteHeader(http.StatusSwitchingProtocols)
	conn, buf, err := http.NewResponseController(rw).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if r.URL.Path != v.path {
		_, _ = io.Copy(io.Discard, buf)
		return
	}
	for _, m := range v.send {
		_, _ = conn.Write(frameOf(0x1, true, false, m, false))
	}
	if v.closeCode != 0 {
		payload := binary.BigEndian.AppendUint16(nil, v.closeCode)
		_, _ = conn.Write(frameOf(0x8, true, false, append(payload, v.closeReason...), false))
	}
	in := bufio.NewReader(io.TeeReader(buf, writerFunc(func(p []byte) (int, error) {
		v.mu.Lock()
		defer v.mu.Unlock()
		return v.got.Write(p)
	})))
	answered := false
	for {
		op, payload, err := readFrame(in)
		switch {
		case err != nil:
			return
		case (op == 0x1 || op == 0x2) && !answered:
			answered = true
			for _, m := range v.answer {
				_, _ = conn.Write(frameOf(0x1, true, false, m, false))
			}
		case op == 0x9:
			_, _ = conn.Write(frameOf(0xa, true, false, payload, false))
		case op == 0x8:
			if v.closeCode == 0 {
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
func (v *scriptedVendor) unchanged() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !bytes.Equal(v.got.Bytes(), v.sent.Bytes()) {
		return failf("the vendor got %d bytes, the app sent %d, and they differ", v.got.Len(), v.sent.Len())
	}
	return nil
}

// appWrite sends bytes from the app, and keeps them.
func (w *world) appWrite(b []byte) error {
	w.scripted.mu.Lock()
	w.scripted.sent.Write(b)
	w.scripted.mu.Unlock()
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
	large := regexp.MustCompile(`^a (packed )?batch_add (?:of|that unpacks to) (\d+) MiB(?: with req_id (\d+))?$`)
	tool := regexp.MustCompile(`^a toolResponse of (\d+) MiB with id "([^"]+)"$`)
	packed := regexp.MustCompile(`^(\d+) add_order messages, packed$`)
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
			reqID, _ := strconv.Atoi(m[3])
			err = w.appMessage(bigBatchAdd(mib<<20, max(reqID, 1)), m[1] != "")
		case tool.MatchString(part):
			m := tool.FindStringSubmatch(part)
			mib, _ := strconv.Atoi(m[1])
			err = w.appMessage(bigToolResponse(mib<<20, m[2]), false)
		case packed.MatchString(part):
			n, _ := strconv.Atoi(packed.FindStringSubmatch(part)[1])
			for i := range n {
				if err = w.appMessage(addOrder(i+1), true); err != nil {
					return err
				}
			}
		default:
			m := plain.FindStringSubmatch(part)
			message, err := scriptedMessage(m[1])
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
	return w.appWrite(frameOf(0x1, true, true, w.scripted.packer.pack(message), true))
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

// krakenTime is the time Kraken's replies carry, the same in every run.
const krakenTime = "2026-11-21T10:00:00.000000Z"

// scriptedMessage is the text of a message written in a scenario: JSON as
// it is, a Kraken message with its req_id, or a message of the tool spec
// with its ids.
func scriptedMessage(text string) ([]byte, error) {
	reqID := regexp.MustCompile(`with req_id (\d+)(?: for "([^"]+)")?$`).FindStringSubmatch(text)
	ids := regexp.MustCompile(`"([^"]+)"`)
	var found []string
	for _, m := range ids.FindAllStringSubmatch(text, -1) {
		found = append(found, m[1])
	}
	reply := func(method, orderID string) []byte {
		return fmt.Appendf(nil, `{"method":%q,"req_id":%s,"success":true,"result":{"order_id":%q},"time_in":%q,"time_out":%q}`,
			method, reqID[1], orderID, krakenTime, krakenTime)
	}
	switch {
	case strings.HasPrefix(text, "{"):
		return []byte(text), nil
	case text == "a heartbeat":
		return []byte(`{"channel":"heartbeat"}`), nil
	case strings.HasPrefix(text, "add_order with req_id ") && reqID != nil:
		n, _ := strconv.Atoi(reqID[1])
		return addOrder(n), nil
	case strings.HasPrefix(text, "add_order's reply with req_id ") && reqID != nil:
		return reply("add_order", cmp.Or(reqID[2], "OQCLML-BW3P3-BUCMWZ")), nil
	case strings.HasPrefix(text, "cancel_order's reply with req_id ") && reqID != nil && reqID[2] != "":
		return reply("cancel_order", reqID[2]), nil
	case strings.HasPrefix(text, "a failed subscribe with req_id ") && reqID != nil:
		return fmt.Appendf(nil, `{"method":"subscribe","req_id":%s,"success":false,"error":"Currency pair not supported NOPE/USD","time_in":%q,"time_out":%q}`,
			reqID[1], krakenTime, krakenTime), nil
	case strings.HasPrefix(text, "toolCall with id"):
		var calls []string
		for _, id := range found {
			calls = append(calls, `{"id":"`+id+`","name":"orderStatus"}`)
		}
		return []byte(`{"toolCall":{"functionCalls":[` + strings.Join(calls, ",") + `]}}`), nil
	case strings.HasPrefix(text, "toolResponse with id"):
		return []byte(`{"toolResponse":{"functionResponses":[{"id":"` + found[0] + `","name":"orderStatus","response":{"status":"sent"}}]}}`), nil
	}
	return nil, failf("no message %q", text)
}

// addOrder is a limit order to buy BTC/USD, with its req_id.
func addOrder(reqID int) []byte {
	return fmt.Appendf(nil, `{"method":"add_order","req_id":%d,"params":{"order_type":"limit","side":"buy","order_qty":1.2,"limit_price":97000.5,"symbol":"BTC/USD","token":"t-not-real"}}`, reqID)
}

// bigBatchAdd is a batch_add of size bytes, its req_id first, then a list
// of orders and their objects that close before its long token.
func bigBatchAdd(size, reqID int) []byte {
	head := fmt.Sprintf(`{"method":"batch_add","req_id":%d,"params":{"symbol":"BTC/USD","orders":[`+
		`{"order_type":"limit","side":"buy","order_qty":1,"limit_price":97000.5},`+
		`{"order_type":"limit","side":"sell","order_qty":1,"limit_price":97010.5}],"token":"`, reqID)
	tail := `"}}`
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

// bigToolResponse is a toolResponse of size bytes, its id first, then
// its long text, inside the list of responses.
func bigToolResponse(size int, id string) []byte {
	head := `{"toolResponse":{"functionResponses":[{"id":"` + id + `","name":"orderStatus","response":{"text":"`
	tail := `"}}]}}`
	return []byte(head + strings.Repeat("a", size-len(head)-len(tail)) + tail)
}

// toolSpec is a shop api whose GET /live switches to a WebSocket and lists
// tool calls sent by the vendor, shaped like Gemini Live's, for the cases
// Kraken has no message for; GET /other switches too, with none listed.
const toolSpec = `openapi: 3.1.0
info: { title: shop tool calls, version: "1" }
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
  schemas:
    toolCall:
      type: object
      required: [toolCall]
      properties:
        toolCall:
          x-hotline-match: true
          type: object
          required: [functionCalls]
          properties:
            functionCalls:
              type: array
              items:
                type: object
                required: [id, name]
                properties:
                  id: { type: string }
                  name: { type: string }
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
            cause: { $ref: "#/components/schemas/toolError" }
`
