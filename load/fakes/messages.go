package fakes

import (
	"maps"
	"sync"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
)

// Messages is what the proxy's WebSocket records said, counted since the
// receiver started.
type Messages struct {
	// Records counts the message records (hotline.websocket.message).
	Records int64
	// Valid and Invalid count the messages whose check ran.
	Valid, Invalid int64
	// Unknown counts the messages that matched no message schema.
	Unknown int64
	// Skipped counts the messages whose check did not run, by
	// hotline.schema.skipped, such as overloaded.
	Skipped map[string]int64
	// Sessions counts the session records, and Unread those of them whose
	// messages could not all be read (hotline.websocket.unread_from_index).
	Sessions, Unread int64
	// Spans counts the spans sent together with a WebSocket's: of calls,
	// sessions and messages. Sends that hold no WebSocket span are not read.
	Spans int64
}

type messageCounts struct {
	mu sync.Mutex
	m  Messages
}

// Messages says what the WebSocket records said so far.
func (r *Receiver) Messages() Messages {
	r.messages.mu.Lock()
	defer r.messages.mu.Unlock()
	m := r.messages.m
	m.Skipped = maps.Clone(m.Skipped)
	return m
}

func (c *messageCounts) readLogs(export *collogs.ExportLogsServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, rl := range export.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, rec := range sl.GetLogRecords() {
				attrs := map[string]*common.AnyValue{}
				for _, kv := range rec.GetAttributes() {
					attrs[kv.GetKey()] = kv.GetValue()
				}
				event := rec.GetEventName()
				if event == "" {
					event = attrs["event.name"].GetStringValue()
				}
				switch event {
				case "hotline.websocket.message":
					c.message(attrs)
				case "hotline.websocket.session":
					c.m.Sessions++
					if _, ok := attrs["hotline.websocket.unread_from_index"]; ok {
						c.m.Unread++
					}
				}
			}
		}
	}
}

func (c *messageCounts) message(attrs map[string]*common.AnyValue) {
	c.m.Records++
	if attrs["hotline.websocket.message.name"].GetStringValue() == "unknown" {
		c.m.Unknown++
	}
	if reason := attrs["hotline.schema.skipped"].GetStringValue(); reason != "" {
		if c.m.Skipped == nil {
			c.m.Skipped = map[string]int64{}
		}
		c.m.Skipped[reason]++
	}
	switch attrs["hotline.schema.status"].GetStringValue() {
	case "valid":
		c.m.Valid++
	case "invalid":
		c.m.Invalid++
	}
}

func (c *messageCounts) readSpans(export *coltrace.ExportTraceServiceRequest) {
	n := int64(0)
	for _, rs := range export.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			n += int64(len(ss.GetSpans()))
		}
	}
	c.mu.Lock()
	c.m.Spans += n
	c.mu.Unlock()
}
