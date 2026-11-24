package proxy_test

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
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
)

// streamingVendor is the fake Anthropic of the streamed response scenarios:
// its spec, what it streams, and every byte it sent.
type streamingVendor struct {
	spec  string
	extra string
	// integration is added to the support integration's YAML, after its apis.
	integration string
	// host is the vendor's host, and contentType what it streams; empty,
	// the fake Anthropic's and Server-Sent Events.
	host        string
	contentType string
	// breaksOff says the vendor breaks off once it has sent everything.
	breaksOff bool
	// at is the place of the integration in the config.
	at int
	// events are the events it streams, each with the blank line that
	// ends it; raw, when set, is sent instead, as written.
	events [][]byte
	raw    []byte
	// oneByte sends the stream one byte at a time.
	oneByte bool
	// breakInside, when not 0, is the event the vendor breaks off in,
	// half way through.
	breakInside int
	// waits says the vendor sends its first event and then waits, for the
	// app to leave or the proxy to stop.
	waits bool
	// restAtStop says the vendor sends its first event, then the rest
	// once the proxy is stopping.
	restAtStop bool
	// whole answers the message as one JSON body.
	whole bool
	// holdsHeaders says the vendor sends its headers only once the proxy
	// is stopping, which it sees when the proxy takes no new connection.
	holdsHeaders bool
	// asked is closed once the vendor holding its headers has the request.
	asked chan struct{}
	addr  string

	mu   sync.Mutex
	sent bytes.Buffer
}

// openStream is the app's end of a stream it keeps open.
type openStream struct {
	conn   net.Conn
	reader *bufio.Reader
}

// The fake Anthropic's host, the app's integration and its api key, which
// no record may hold.
const (
	anthropicHost = "api.anthropic.com"
	anthropicApp  = "support"
	anthropicKey  = "sk-ant-api03-not-a-real-key"
)

// anthropicSpec is the Anthropic spec, as the repository holds it.
const anthropicSpec = "../../specs/anthropic/messages/2023-06-01.openapi.yaml"

// The made-up order feed, streamed as JSON Lines or JSON sequences.
const (
	feedHost = "feed.shop.test"
	feedSpec = "testdata/streams/order-feed.openapi.yaml"
)

// helloResponse is the stream of Anthropic's streaming docs, which says
// "Hello!": eight events.
func helloResponse() [][]byte {
	data := []struct{ event, data string }{
		{"message_start", `{"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-opus-5-5", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 25, "output_tokens": 1}}}`},
		{"content_block_start", `{"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}`},
		{"ping", `{"type": "ping"}`},
		{"content_block_delta", `{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}`},
		{"content_block_delta", `{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "!"}}`},
		{"content_block_stop", `{"type": "content_block_stop", "index": 0}`},
		{"message_delta", `{"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 15}}`},
		{"message_stop", `{"type": "message_stop"}`},
	}
	events := make([][]byte, len(data))
	for i, d := range data {
		events[i] = sseEvent(d.event, d.data)
	}
	return events
}

func sseEvent(name, data string) []byte {
	return []byte("event: " + name + "\ndata: " + data + "\n\n")
}

// helloMessage is the same answer, sent whole.
const helloMessage = `{"id":"msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"claude-opus-5-5","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":15}}`

func streamSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the Anthropic spec$`, func() error {
		spec, err := os.ReadFile(filepath.FromSlash(anthropicSpec))
		if err != nil {
			return err
		}
		w.streaming = &streamingVendor{spec: string(spec), at: len(w.integrations)}
		w.addIntegration("")
		w.writeStreamingSpec()
		w.vendor(anthropicHost).setAnswer(w.streamingVendor)
		return nil
	})
	sc.Step(`^the order feed spec$`, w.useFeedSpec)
	sc.Step(`^the order feed spec, without the itemSchema of application/jsonl$`, func() error {
		if err := w.useFeedSpec(); err != nil {
			return err
		}
		cut := "            application/jsonl:\n              itemSchema:\n                $ref: \"#/components/schemas/Order\"\n"
		if !strings.Contains(w.streaming.spec, cut) {
			return failf("the order feed spec has no itemSchema for application/jsonl to take out")
		}
		w.streaming.spec = strings.Replace(w.streaming.spec, cut, "            application/jsonl: {}\n", 1)
		w.writeStreamingSpec()
		return nil
	})
	sc.Step(`^the feed vendor streams (\S+) bytes (?s)(".*")$`, func(contentType, quoted string) error {
		w.streaming.contentType = contentType
		return w.streamBytes(quoted)
	})
	sc.Step(`^the feed vendor streams (\S+) bytes (".*"), then breaks off$`, func(contentType, quoted string) error {
		w.streaming.contentType, w.streaming.breaksOff = contentType, true
		return w.streamBytes(quoted)
	})
	sc.Step(`^the feed vendor streams a JSON Lines order of 3 MiB, then one small order$`, func() error {
		head, tail := `{"id":"A-1001","total":1,"note":"`, `"}`+"\n"
		big := head + strings.Repeat("a", 3<<20-len(head)-len(tail)) + tail
		w.streaming.contentType = "application/jsonl"
		w.streaming.events = [][]byte{[]byte(big), []byte(`{"id":"A-1002","total":2}` + "\n")}
		return nil
	})
	sc.Step(`^the app reads the order feed$`, func() error {
		return w.send(request{method: "GET", target: "https://" + feedHost + "/orders/feed", headers: []string{basic(anthropicApp + ":x")}, length: -1})
	})
	sc.Step(`^the Anthropic spec, without the itemSchema$`, func() error {
		cut := "            text/event-stream:\n              itemSchema:\n                $ref: \"#/components/schemas/StreamEvent\"\n"
		if !strings.Contains(w.streaming.spec, cut) {
			return failf("the Anthropic spec has no itemSchema to take out")
		}
		w.streaming.spec = strings.Replace(w.streaming.spec, cut, "            text/event-stream: {}\n", 1)
		w.writeStreamingSpec()
		return nil
	})
	sc.Step(`^the Anthropic spec, checking only headers and status$`, func() error {
		w.streaming.extra = "    checks: [headers, status]\n"
		w.writeStreamingSpec()
		return nil
	})
	sc.Step(`^the Anthropic vendor streams the response "Hello!"$`, func() error {
		w.streaming.events = helloResponse()
		return nil
	})
	sc.Step(`^the Anthropic vendor streams the response "Hello!", one byte at a time$`, func() error {
		w.streaming.events, w.streaming.oneByte = helloResponse(), true
		return nil
	})
	sc.Step(`^the Anthropic vendor streams the response "Hello!", with event (\d+)'s data (.+)$`, func(n int, data string) error {
		events := helloResponse()
		name := strings.TrimPrefix(strings.SplitN(string(events[n-1]), "\n", 2)[0], "event: ")
		events[n-1] = sseEvent(name, data)
		w.streaming.events = events
		return nil
	})
	sc.Step(`^the Anthropic vendor streams the response "Hello!", breaking off inside event (\d+)$`, func(n int) error {
		w.streaming.events, w.streaming.breakInside = helloResponse(), n
		return nil
	})
	sc.Step(`^the Anthropic vendor streams the bytes (?s)(".*")$`, w.streamBytes)
	sc.Step(`^the Anthropic vendor streams a content_block_delta of 3 MiB, then the response "Hello!"$`, func() error {
		head := `event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`
		tail := `"}}` + "\n\n"
		big := head + strings.Repeat("a", 3<<20-len(head)-len(tail)) + tail
		w.streaming.events = append([][]byte{[]byte(big)}, helloResponse()...)
		return nil
	})
	sc.Step(`^the Anthropic vendor streams message_start, then waits for the app to read it$`, func() error {
		w.streaming.events, w.streaming.waits = helloResponse()[:1], true
		return nil
	})
	sc.Step(`^the Anthropic vendor streams message_start, then the rest of the response "Hello!" once the proxy is stopping$`, func() error {
		w.streaming.events, w.streaming.restAtStop = helloResponse(), true
		return nil
	})
	sc.Step(`^the Anthropic vendor holds its headers until the proxy is stopping, then streams message_start$`, func() error {
		w.streaming.events, w.streaming.holdsHeaders, w.streaming.waits = helloResponse()[:1], true, true
		w.streaming.asked = make(chan struct{})
		return nil
	})
	sc.Step(`^the Anthropic spec, where every event needs a retry$`, func() error {
		from := "    StreamEvent:\n      type: object\n      required: [event, data]\n"
		if !strings.Contains(w.streaming.spec, from) {
			return failf("the Anthropic spec has no StreamEvent to change")
		}
		w.streaming.spec = strings.Replace(w.streaming.spec, from, "    StreamEvent:\n      type: object\n      required: [event, data, retry]\n", 1)
		w.writeStreamingSpec()
		return nil
	})
	sc.Step(`^the stream's "([^"]+)" is (\w+)$`, func(text, colour string) error {
		return w.consoleHas("\x1b[" + colourCode(colour) + "m" + text + "\x1b[0m")
	})
	sc.Step(`^the Anthropic vendor answers the message "Hello!" whole$`, func() error {
		w.streaming.whole = true
		return nil
	})
	sc.Step(`^the app asks for a streamed message$`, func() error {
		return w.send(anthropicRequest(true))
	})
	sc.Step(`^the app asks for a message$`, func() error {
		return w.send(anthropicRequest(false))
	})
	sc.Step(`^the app sends a large order, then asks for a streamed message$`, func() error {
		if err := w.largeOrder(); err != nil {
			return err
		}
		return w.send(anthropicRequest(true))
	})
	sc.Step(`^the app asks for a streamed message and reads 1 event$`, w.openStreamAndReadOne)
	sc.Step(`^the app asks for a streamed message, without waiting for it$`, func() error {
		if err := w.start(); err != nil {
			return err
		}
		w.streaming.addr = w.addr
		if err := w.askOnOwnConnection(); err != nil {
			return err
		}
		// The call must reach the vendor before the proxy stops taking
		// connections, or there is no stream to open.
		select {
		case <-w.streaming.asked:
			return nil
		case <-time.After(2 * time.Second):
			return failf("the vendor did not get the request within 2 seconds")
		}
	})
	sc.Step(`^the app got every byte as the vendor sent it$`, func() error {
		w.settle()
		if w.resp == nil || w.resp.StatusCode != http.StatusOK {
			return failf("the app got %v, %v", w.resp, w.respErr)
		}
		w.streaming.mu.Lock()
		defer w.streaming.mu.Unlock()
		if !bytes.Equal(w.respBody, w.streaming.sent.Bytes()) {
			return failf("the app got %d bytes, the vendor sent %d", len(w.respBody), w.streaming.sent.Len())
		}
		return nil
	})
	sc.Step(`^a call record and an stream item record arrive while the stream is open$`, func() error {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if len(w.receivers[0].got()) > 0 && len(w.receivers[0].streamItemRecords()) > 0 {
				if len(w.receivers[0].streamRecords()) > 0 {
					return failf("the stream record arrived while the stream was open")
				}
				return nil
			}
		}
		return failf("no call record and stream item record arrived while the stream was open")
	})
	sc.Step(`^the stream's call record has no response body, and the hash of no body$`, func() error {
		call, err := w.callRecord()
		if err != nil {
			return err
		}
		return errors.Join(has(call.attrs, "http.response.body.size", 0),
			has(call.attrs, "hotline.response.body.hash", bodyHash("response", call.traceID, call.spanID, nil)))
	})
	sc.Step(`^the stream's call record says (.+)$`, func(pairs string) error {
		call, err := w.callRecord()
		if err != nil {
			return err
		}
		return says(call.attrs, pairs)
	})
	sc.Step(`^there are (\d+) stream item records$`, func(n int) error {
		if got := w.streamItemRecords(); len(got) != n {
			return failf("there are %d stream item records, want %d", len(got), n)
		}
		return nil
	})
	sc.Step(`^there are (\d+) stream item records, each (.+)$`, func(n int, pairs string) error {
		got := w.streamItemRecords()
		if len(got) != n {
			return failf("there are %d stream item records, want %d", len(got), n)
		}
		var errs []error
		for i, r := range got {
			if err := says(r.attrs, pairs); err != nil {
				errs = append(errs, fmt.Errorf("stream item record %d: %w", i+1, err))
			}
		}
		return errors.Join(errs...)
	})
	sc.Step(`^stream item record (\d+) says (.+)$`, func(n int, pairs string) error {
		r, err := w.streamItemRecord(n)
		if err != nil {
			return err
		}
		return says(r.attrs, pairs)
	})
	sc.Step(`^stream item records (\d+) to (\d+) say (.+)$`, func(from, to int, pairs string) error {
		var errs []error
		for n := from; n <= to; n++ {
			r, err := w.streamItemRecord(n)
			if err == nil {
				err = says(r.attrs, pairs)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("stream item record %d: %w", n, err))
			}
		}
		return errors.Join(errs...)
	})
	sc.Step(`^stream item record (\d+) has no (.+)$`, func(n int, keys string) error {
		r, err := w.streamItemRecord(n)
		if err != nil {
			return err
		}
		return hasNo(r.attrs, regexp.MustCompile(`, | or `).Split(keys, -1)...)
	})
	sc.Step(`^stream item record (\d+)'s response results are (.+)$`, func(n int, want string) error {
		r, err := w.streamItemRecord(n)
		if err != nil {
			return err
		}
		got, err := resultsOf(r.attrs, "response")
		if err != nil {
			return err
		}
		if strings.Join(got, "; ") != want {
			return failf("the results are %q, want %q", strings.Join(got, "; "), want)
		}
		return nil
	})
	sc.Step(`^the stream record says (.+)$`, func(pairs string) error {
		s, err := w.streamRecord()
		if err != nil {
			return err
		}
		return says(s.attrs, pairs)
	})
	sc.Step(`^the ledger holds the call first, then the (\d+) items, then the stream$`, func(n int) error {
		call, err := w.callRecord()
		if err != nil {
			return err
		}
		stream, err := w.streamRecord()
		if err != nil {
			return err
		}
		items := w.streamItemRecords()
		if len(items) != n {
			return failf("want %d stream item records, got %d", n, len(items))
		}
		// Items may reach the ledger in any order, as their checks end.
		for _, item := range items {
			if ledgerPlace(item) <= ledgerPlace(call) || ledgerPlace(item) >= ledgerPlace(stream) {
				return failf("item %v is in the ledger at %s, not between the call at %s and the stream at %s",
					item.attrs["hotline.stream.item.index"], ledgerPlace(item), ledgerPlace(call), ledgerPlace(stream))
			}
		}
		return nil
	})
	sc.Step(`^there is no stream record$`, func() error {
		w.settle()
		if got := w.receivers[0].streamRecords(); len(got) > 0 {
			return failf("there are %d stream records, want none", len(got))
		}
		return nil
	})
	sc.Step(`^there is one call record and one stream record$`, func() error {
		if _, err := w.callRecord(); err != nil {
			return err
		}
		_, err := w.streamRecord()
		return err
	})
	sc.Step(`^the stream record's hotline.stream.size is the size of the response "Hello!", and its hotline.stream.hash the hash of it$`, func() error {
		s, err := w.streamRecord()
		if err != nil {
			return err
		}
		sent := bytes.Join(helloResponse(), nil)
		return errors.Join(has(s.attrs, "hotline.stream.size", len(sent)),
			has(s.attrs, "hotline.stream.hash", bodyHash("stream", s.traceID, s.spanID, sent)))
	})
	sc.Step(`^the stream record's span is a "stream" span under the call's "hotline call" span, starting when the call ends$`, func() error {
		call, err := w.callRecord()
		if err != nil {
			return err
		}
		s, err := w.streamRecord()
		if err != nil {
			return err
		}
		callSpan, streamSpan := w.spanByID(call.spanID), w.spanByID(s.spanID)
		switch {
		case callSpan == nil || streamSpan == nil:
			return failf("the receiver has the call span %v and the stream span %v", callSpan, streamSpan)
		case callSpan.name != "hotline call" || streamSpan.name != "stream":
			return failf("the spans are named %q and %q", callSpan.name, streamSpan.name)
		case streamSpan.parent != callSpan.spanID || streamSpan.traceID != callSpan.traceID || s.traceID != call.traceID:
			return failf("the stream span's parent is %s, want %s", streamSpan.parent, callSpan.spanID)
		case streamSpan.start != callSpan.end || streamSpan.end < streamSpan.start:
			return failf("the stream span runs %d to %d, the call span ends at %d", streamSpan.start, streamSpan.end, callSpan.end)
		}
		return nil
	})
	sc.Step(`^each stream item record's span is a "stream item" span under the "stream" span$`, func() error {
		s, err := w.streamRecord()
		if err != nil {
			return err
		}
		items := w.streamItemRecords()
		if len(items) == 0 {
			return failf("there are no stream item records")
		}
		for i, r := range items {
			span := w.spanByID(r.spanID)
			if span == nil || span.name != "stream item" || span.parent != s.spanID || r.traceID != s.traceID {
				return failf("stream item record %d's span is %v, want a \"stream item\" span under %s", i+1, span, s.spanID)
			}
		}
		return nil
	})
	sc.Step(`^each "stream item" span has an "item spec check" span under it, with a "wait for check worker" span under that$`, func() error {
		w.settle()
		spans := w.receivers[0].spans()
		items := 0
		for _, item := range spans {
			if item.name != "stream item" {
				continue
			}
			items++
			check := slices.IndexFunc(spans, func(s receivedSpan) bool { return s.name == "item spec check" && s.parent == item.spanID })
			if check < 0 {
				return failf("a \"stream item\" span has no \"item spec check\" span under it")
			}
			if !slices.ContainsFunc(spans, func(s receivedSpan) bool {
				return s.name == "wait for check worker" && s.parent == spans[check].spanID
			}) {
				return failf("an \"item spec check\" span has no \"wait for check worker\" span under it")
			}
		}
		if items == 0 {
			return failf("no \"stream item\" span arrived")
		}
		return nil
	})
	sc.Step(`^the fourth line is "([^"]*)" with the stream's size and the time it was open$`, func(want string) error {
		return w.consoleLine(3, `^ *`+regexp.QuoteMeta(want)+` \d+(\.\d)? (B|KiB|MiB) \d+\.\d ms$`)
	})
	sc.Step(`^the support integration has content full and maxBodySize (\S+)$`, func(size string) error {
		w.streaming.integration = "    content: full\n    maxBodySize: " + size + "\n"
		w.writeStreamingSpec()
		return nil
	})
	sc.Step(`^the stream line's hotline.content.stream.body is the response "Hello!" as the vendor sent it$`, func() error {
		return w.streamLineKeeps(bytes.Join(helloResponse(), nil), false)
	})
	sc.Step(`^the stream line's hotline.content.stream.body is the first 256 bytes of the response "Hello!", with hotline.content.stream.body.truncated true$`, func() error {
		return w.streamLineKeeps(bytes.Join(helloResponse(), nil)[:256], true)
	})
	sc.Step(`^no record or span holds the app's api key$`, func() error {
		w.settle()
		r := w.receivers[0]
		var all strings.Builder
		for _, list := range [][]receivedRecord{r.got(), r.streamRecords(), r.streamItemRecords()} {
			for _, rec := range list {
				all.WriteString(sprint(rec.attrs))
			}
		}
		for _, s := range r.spans() {
			all.WriteString(sprint(s.attrs))
		}
		all.WriteString(w.logs.String())
		if strings.Contains(all.String(), anthropicKey) {
			return failf("the api key appears in the records, spans or log")
		}
		return nil
	})
}

// anthropicRequest is the app's call for a message, streamed or whole.
func anthropicRequest(stream bool) request {
	return anthropicMessage(stream, `"Hi"`)
}

// anthropicMessage asks for a message whose one user message has the
// content given, as JSON.
func anthropicMessage(stream bool, content string) request {
	body := fmt.Sprintf(`{"model":"claude-opus-5-5","max_tokens":64,"stream":%t,"messages":[{"role":"user","content":%s}]}`, stream, content)
	return request{
		method: "POST",
		target: "https://" + anthropicHost + "/v1/messages",
		headers: []string{
			basic(anthropicApp + ":x"),
			"x-api-key: " + anthropicKey,
			"anthropic-version: 2023-06-01",
			"Content-Type: application/json",
		},
		body:   strings.NewReader(body),
		length: len(body),
	}
}

// useFeedSpec puts the order feed in the Anthropic spec's place in the
// support integration.
func (w *world) useFeedSpec() error {
	spec, err := os.ReadFile(filepath.FromSlash(feedSpec))
	if err != nil {
		return err
	}
	w.streaming.spec, w.streaming.host = string(spec), feedHost
	w.writeStreamingSpec()
	w.vendor(feedHost).setAnswer(w.streamingVendor)
	return nil
}

// streamBytes has the vendor stream the bytes, quoted as Go quotes them.
func (w *world) streamBytes(quoted string) error {
	// A table cell turns \n into a new line; the rest is Go's quoting.
	raw, err := strconv.Unquote(strings.ReplaceAll(quoted, "\n", `\n`))
	if err != nil {
		return err
	}
	w.streaming.raw = []byte(raw)
	return nil
}

// writeStreamingSpec writes the fake Anthropic's spec and puts its
// integration in its place.
func (w *world) writeStreamingSpec() {
	v := w.streaming
	w.integrations[v.at] = integration(anthropicApp, w.specAPI(cmp.Or(v.host, anthropicHost), v.spec, v.extra)) + v.integration
}

// streamingVendor answers as the fake Anthropic: the whole message, or its
// events, each sent and flushed on its own.
func (w *world) streamingVendor(rw http.ResponseWriter, r *http.Request) {
	v := w.streaming
	_, _ = io.Copy(io.Discard, r.Body)
	if v.whole {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(rw, helloMessage)
		return
	}
	if v.holdsHeaders {
		close(v.asked)
		v.waitForStop()
	}
	rw.Header().Set("Content-Type", cmp.Or(v.contentType, "text/event-stream"))
	rw.Header().Set("Cache-Control", "no-cache")
	rw.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(rw).Flush
	_ = flush()
	v.mu.Lock()
	pieces := v.events
	if v.raw != nil {
		pieces = [][]byte{v.raw}
	}
	v.mu.Unlock()
	for i, piece := range pieces {
		if v.breakInside == i+1 {
			v.write(rw, flush, piece[:len(piece)/2])
			// The vendor breaks off: the app must see a broken response.
			panic(http.ErrAbortHandler)
		}
		v.write(rw, flush, piece)
		if v.restAtStop && i == 0 {
			v.waitForStop()
		}
	}
	if v.breaksOff {
		panic(http.ErrAbortHandler)
	}
	if v.waits {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// write sends a piece of the stream, whole or one byte at a time, and
// keeps what was sent.
func (v *streamingVendor) write(rw http.ResponseWriter, flush func() error, piece []byte) {
	step := len(piece)
	if v.oneByte {
		step = 1
	}
	for len(piece) > 0 {
		n := min(step, len(piece))
		if _, err := rw.Write(piece[:n]); err != nil {
			return
		}
		v.mu.Lock()
		v.sent.Write(piece[:n])
		v.mu.Unlock()
		_ = flush()
		piece = piece[n:]
	}
}

// waitForStop waits, at most 2 seconds, until the proxy takes no new
// connection: it is stopping. A vendor that never sees the stop breaks
// off, so the scenario fails instead of passing without a stop.
func (v *streamingVendor) waitForStop() {
	if !stopsWithin(v.addr, 2*time.Second) {
		panic(http.ErrAbortHandler)
	}
}

// openStreamAndReadOne asks for a streamed message on a connection of its
// own, reads the first event and keeps the stream open.
func (w *world) openStreamAndReadOne() error {
	if err := w.askOnOwnConnection(); err != nil {
		return err
	}
	_ = w.open.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// The stream stays open: closing its body would read it to the end.
	// The scenario's end closes the connection instead.
	resp, err := http.ReadResponse(w.open.reader, nil) //nolint:bodyclose // see above
	if err != nil {
		return err
	}
	first, err := bufio.NewReader(resp.Body).ReadString('}')
	if err != nil || !strings.Contains(first, "message_start") {
		return failf("the app read %q, %v; want the first event", first, err)
	}
	return nil
}

// askOnOwnConnection sends the app's ask for a streamed message on a
// connection of its own, which stays open.
func (w *world) askOnOwnConnection() error {
	if err := w.start(); err != nil {
		return err
	}
	w.vendor(anthropicHost)
	w.streaming.addr = w.addr
	conn, err := w.dialProxy()
	if err != nil {
		return err
	}
	r := anthropicRequest(true)
	var head bytes.Buffer
	fmt.Fprintf(&head, "%s %s HTTP/1.1\r\nHost: %s\r\n", r.method, r.target, anthropicHost)
	for _, h := range r.headers {
		head.WriteString(h + "\r\n")
	}
	fmt.Fprintf(&head, "Content-Length: %d\r\n\r\n", r.length)
	body, _ := io.ReadAll(r.body)
	head.Write(body)
	if _, err := conn.Write(head.Bytes()); err != nil {
		_ = conn.Close()
		return err
	}
	w.open = &openStream{conn: conn, reader: bufio.NewReader(conn)}
	return nil
}

// ledgerPlace is where a record sits in the ledger, as text that sorts
// in the ledger's order: its batch, then its index in the batch.
func ledgerPlace(r receivedRecord) string {
	return fmt.Sprintf("%v/%09d", r.attrs["hotline.batch.id"], r.attrs["hotline.batch.index"])
}

// callRecord is the one call record, once the proxy has sent everything.
func (w *world) callRecord() (receivedRecord, error) {
	calls := w.records()
	var mine []receivedRecord
	for _, c := range calls {
		if c.attrs["hotline.integration.id"] == anthropicApp {
			mine = append(mine, c)
		}
	}
	if len(mine) != 1 {
		return receivedRecord{}, failf("there are %d call records of %s, want one", len(mine), anthropicApp)
	}
	return mine[0], nil
}

// streamRecord is the one stream record, once the proxy has sent
// everything.
func (w *world) streamRecord() (receivedRecord, error) {
	w.settle()
	streams := w.receivers[0].streamRecords()
	if len(streams) != 1 {
		return receivedRecord{}, failf("there are %d stream records, want one", len(streams))
	}
	return streams[0], nil
}

// streamItemRecords are the stream item records, by their index, once the proxy has
// sent everything.
func (w *world) streamItemRecords() []receivedRecord {
	w.settle()
	records := w.receivers[0].streamItemRecords()
	slices.SortFunc(records, func(a, b receivedRecord) int {
		return strings.Compare(fmt.Sprintf("%09v", a.attrs["hotline.stream.item.index"]), fmt.Sprintf("%09v", b.attrs["hotline.stream.item.index"]))
	})
	return records
}

// streamItemRecord is the stream item record with index n.
func (w *world) streamItemRecord(n int) (receivedRecord, error) {
	records := w.streamItemRecords()
	if n < 1 || n > len(records) {
		return receivedRecord{}, failf("there are %d stream item records, want a record %d", len(records), n)
	}
	r := records[n-1]
	return r, has(r.attrs, "hotline.stream.item.index", n)
}

// spanByID is the span with the id, nil when none arrived.
func (w *world) spanByID(id string) *receivedSpan {
	w.settle()
	for _, s := range w.receivers[0].spans() {
		if s.spanID == id {
			return &s
		}
	}
	return nil
}

// streamLineKeeps checks the stream body the file's stream line keeps, and
// whether it says the stream was cut short.
func (w *world) streamLineKeeps(want []byte, cut bool) error {
	lines, err := w.allFileLines(0)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if line["event"] != "hotline.stream" {
			continue
		}
		attrs, _ := line["attributes"].(map[string]any)
		got, err := base64.StdEncoding.DecodeString(sprint(attrs["hotline.content.stream.body"]))
		if err != nil || !bytes.Equal(got, want) {
			return failf("the stream line keeps %q, want %q", got, want)
		}
		if truncated, _ := attrs["hotline.content.stream.body.truncated"].(bool); truncated != cut {
			return failf("the stream line's truncated flag is %v, want %v", truncated, cut)
		}
		return nil
	}
	return failf("the file has no stream line")
}
