// Package fakeanthropic is a fake of Anthropic's Messages api for the
// shop's tests: it answers each request with the next scripted response,
// streamed as Server-Sent Events, the way Anthropic streams one, and keeps
// each request it got.
package fakeanthropic

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Anthropic is the fake. Its Requests and Keys methods give the body and
// the Authorization header of each request it got, in order.
type Anthropic struct {
	*httptest.Server

	mu        sync.Mutex
	responses [][]string
	requests  []map[string]any
	keys      []string
}

// Start starts the fake, which answers with the responses in order. A
// request past the last gets status 500.
func Start(responses ...[]string) *Anthropic {
	a := &Anthropic{responses: responses}
	a.Server = httptest.NewServer(http.HandlerFunc(a.serve))
	return a
}

// Requests are the bodies of the requests so far.
func (a *Anthropic) Requests() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests
}

// Keys are the Authorization headers of the requests so far: the SDK sends
// the api key as a bearer token.
func (a *Anthropic) Keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.keys
}

func (a *Anthropic) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	a.mu.Lock()
	a.requests = append(a.requests, body)
	a.keys = append(a.keys, r.Header.Get("Authorization"))
	var events []string
	if len(a.responses) > 0 {
		events, a.responses = a.responses[0], a.responses[1:]
	}
	a.mu.Unlock()
	if events == nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"no more responses"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flush := http.NewResponseController(w).Flush
	for _, e := range events {
		if e == breakOff {
			panic(http.ErrAbortHandler) // the connection drops mid-response
		}
		_, _ = io.WriteString(w, e)
		_ = flush()
	}
}

// Text is a response that streams text in pieces, then ends the turn.
func Text(pieces ...string) []string {
	events := []string{start(), event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)}
	for _, p := range pieces {
		events = append(events, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+quote(p)+`}}`))
	}
	return append(events, end("end_turn")...)
}

// ToolUse is a response that asks for one tool, with its input as JSON
// streamed in two pieces.
func ToolUse(id, name, input string) []string {
	half := len(input) / 2
	return append([]string{
		start(),
		event("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name)),
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quote(input[:half])+`}}`),
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quote(input[half:])+`}}`),
	}, end("tool_use")...)
}

// breakOff in a response drops the connection there.
const breakOff = "break off"

// TextThenToolUse is a response that says some text, then asks for one
// tool, as Claude often does.
func TextThenToolUse(text, id, name, input string) []string {
	return append([]string{
		start(),
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+quote(text)+`}}`),
		event("content_block_stop", `{"type":"content_block_stop","index":0}`),
		event("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":%q,"name":%q,"input":{}}}`, id, name)),
		event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+quote(input)+`}}`),
		event("content_block_stop", `{"type":"content_block_stop","index":1}`),
	}, end("tool_use")[1:]...)
}

// BreaksOff is the start of a response whose connection drops before it
// ends.
func BreaksOff() []string {
	return []string{start(), event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`), breakOff}
}

func start() string {
	return event("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"output_tokens":1}}}`)
}

func end(reason string) []string {
	return []string{
		event("content_block_stop", `{"type":"content_block_stop","index":0}`),
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`","stop_sequence":null},"usage":{"output_tokens":15}}`),
		event("message_stop", `{"type":"message_stop"}`),
	}
}

// quote writes a text as a JSON string.
func quote(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err) // a Go string always marshals
	}
	return string(q)
}

func event(name, data string) string {
	return "event: " + name + "\ndata: " + strings.TrimSpace(data) + "\n\n"
}
