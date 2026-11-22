package spec

import (
	"mime"
	"net/http"
	"strings"
)

// eventStream is the media type of Server-Sent Events, the one streamed
// media type read stream item by stream item so far (roadmap M2). JSON Lines and JSON
// sequences come next.
const eventStream = "text/event-stream"

// ItemSchema checks the stream items of one streamed response, each on its own, by the
// itemSchema that OpenAPI 3.2 gives its media type (Peter, 2026-11-22, M2).
type ItemSchema struct {
	check ResponseCheck
}

// ItemSchema is the itemSchema of a response on the route, by its status and
// Content-Type. It is nil when the spec gives none, or when the media type
// is not one read stream item by stream item.
func (s *Spec) ItemSchema(found Route, status int, header http.Header) *ItemSchema {
	r := found.r
	if r.checks == nil || r.checks.responses == nil {
		return nil
	}
	rp := r.checks.response(status)
	if rp == nil {
		return nil
	}
	mt, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	return rp.itemSchemas[mt]
}

// Check checks one stream item, written as JSON. For Server-Sent Events that is
// the object of the event's fields: data, event, id and retry. Its findings
// are on the response side, as for a response's body, and its fields marked
// x-hotline-record are copied.
func (i *ItemSchema) Check(item []byte) Checked {
	findings, copied := i.check.Body(0, ReadsJSON, item)
	return Checked{Response: findings, Copied: copied}
}

// itemSchemas builds the stream item checks of a response's streamed media types that
// give an itemSchema. Other media types' itemSchema load and are skipped
// quietly, as every 3.2 part the proxy can't check yet (Peter,
// 2026-11-21, M0).
func (b *builder) itemSchemas(m map[string]any, pointer, where, code string) map[string]*ItemSchema {
	listed, _ := m["content"].(map[string]any)
	var found map[string]*ItemSchema
	for mediaType, v := range listed {
		media, _ := v.(map[string]any)
		schema, has := media["itemSchema"]
		if !has || strings.ToLower(mediaType) != eventStream {
			continue
		}
		at := pointer + "/content" + token(mediaType) + "/itemSchema"
		ct := content{
			mediaType: "application/json",
			schema:    schemaAt{schema: schema, found: true, pointer: at, label: "an item of the " + code + " response " + mediaType},
			fields:    b.fields(schema, at),
		}
		if found == nil {
			found = map[string]*ItemSchema{}
		}
		found[eventStream] = &ItemSchema{check: b.responseCheck(&response{body: &bodySpec{content: []content{ct}}}, where)}
	}
	return found
}
