package spec

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"mime"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Messages are the messages each side of a WebSocket may send, as the
// operation that answers 101 lists them under x-async-api, in AsyncAPI 3's
// own shapes (Peter, 2026-11-21, Q1 and Q7):
//
//	x-async-api:
//	  operations:
//	    toolCall:
//	      action: send   # the vendor sends it; receive: the app sends it
//	      messages: [{ $ref: '#/components/x-async-api/messages/toolCall' }]
//	      reply:
//	        messages: [{ $ref: '#/components/x-async-api/messages/toolResponse' }]
//
// The messages sit in components/x-async-api/messages, each with its
// payload, a $ref to components/schemas, and maybe a correlationId and a
// contentType. A message is named by the fields its payload schema marks
// x-hotline-match, and checked against that schema with the same checker
// as call bodies. It never changes after Parse.
type Messages struct {
	app, vendor side
}

// side is what one side of a session may send.
type side struct {
	// readable says the side's content type is JSON, which is the only kind
	// of message the proxy reads.
	readable bool
	kinds    []kind
	// wanted is what naming reads of a message.
	wanted *wanted
}

// kind is one message: its name in components/x-async-api/messages, its
// operation and role, the marks that find it, where its correlation id is
// and its check.
type kind struct {
	name      string
	operation string
	role      string
	matches   []matchMark
	// correlation is the JSON pointer of the correlation id, split into
	// its steps; nil when the message has none.
	correlation []string
	check       ResponseCheck
}

// matchMark is a field marked x-hotline-match: it must be there and, when
// its schema has a const, have that value.
type matchMark struct {
	path     []string
	value    any
	hasValue bool
}

// Message is one message, as the step that names it found it. That step
// is never dropped, so its name and correlation id are known even when
// the message's check is skipped (Peter, 2026-11-19, Q14).
type Message struct {
	// Name is the message's name, or unknown when no message's marks all
	// match.
	Name string
	// Operation is the name of the operation the message belongs to, and
	// Role what it is there: request, reply, answer or push (Peter,
	// 2026-11-21, Q6 and Q9). Both are empty for an unknown message.
	Operation, Role string
	// ConversationID is the message's correlation id, when the message has
	// one and its value is text or a number (Q3).
	ConversationID string
	// Skipped says why the message can't be checked at all, such as
	// unsupported_content_type.
	Skipped string
	kind    *kind
}

// Unknown names a message no schema's marks match.
const Unknown = "unknown"

// The roles of a message, from where the spec lists it (Q9).
const (
	// roleRequest is in an operation's messages, and the operation has a
	// reply.
	roleRequest = "request"
	// roleReply is in an operation's reply: it answers a request of that
	// same operation.
	roleReply = "reply"
	// roleAnswer has a correlation id but is in no reply, and its operation
	// has none: it answers a request of any operation.
	roleAnswer = "answer"
	// rolePush is tied to no request.
	rolePush = "push"
)

// Messages are the messages of the route's WebSocket; nil when the route
// lists none, so the session is counted in bytes only.
func (f Route) Messages() *Messages {
	if f.r.checks == nil {
		return nil
	}
	return f.r.checks.messages
}

// Name finds which message data is: the first message of the side, by
// operation name and then as the operation lists them, whose marked fields
// all match. A message cut at the size kept is named
// from the part kept.
func (m *Messages) Name(fromApp bool, data []byte) Message {
	s := m.side(fromApp)
	if !s.readable {
		return Message{Name: Unknown, Skipped: "unsupported_content_type"}
	}
	v := s.wanted.read(data)
	for i := range s.kinds {
		k := &s.kinds[i]
		if k.matched(v) {
			return Message{Name: k.name, Operation: k.operation, Role: k.role, ConversationID: k.conversationIn(v), kind: k}
		}
	}
	return Message{Name: Unknown}
}

// Check checks a named message, whole and not unknown, against its schema,
// and copies its fields marked x-hotline-record. A message from the app is
// checked as a request body is, and one from the vendor as a response
// body: their results are on that side of what is found.
func (m *Messages) Check(msg Message, fromApp bool, data []byte) Checked {
	results, copied := msg.kind.check.Body(0, ReadsJSON, data)
	c := Checked{Copied: copied}
	if fromApp {
		c.Request = results
	} else {
		c.Response = results
	}
	return c
}

func (m *Messages) side(fromApp bool) *side {
	if fromApp {
		return &m.app
	}
	return &m.vendor
}

// matched says whether every marked field of the message is there, with
// its const value when it has one.
func (k *kind) matched(v any) bool {
	for _, mark := range k.matches {
		got, found := at(v, mark.path)
		if !found || mark.hasValue && !reflect.DeepEqual(got, mark.value) {
			return false
		}
	}
	return true
}

// conversationIn is the message's correlation id: the value at its
// pointer, when that is text or a number.
func (k *kind) conversationIn(v any) string {
	if k.correlation == nil {
		return ""
	}
	for _, name := range k.correlation {
		switch x := v.(type) {
		case map[string]any:
			v = x[name]
		case []any:
			i, err := strconv.Atoi(name)
			if err != nil || i < 0 || i >= len(x) {
				return ""
			}
			v = x[i]
		default:
			return ""
		}
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}

// at is the value at a path of properties.
func at(v any, path []string) (any, bool) {
	for _, name := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[name]; !ok {
			return nil, false
		}
	}
	return v, true
}

// wanted is the part of a side's messages that naming reads: the fields
// its marks name, and nothing else. A Gemini Live message is mostly
// audio no mark looks at, and reading all of it, then reading it again
// for the check, handed out over half of a session's memory (load test,
// 2026-11-20).
type wanted struct {
	// whole keeps the value here as it is: a const to compare, or a
	// correlation id.
	whole bool
	// props are the properties read further, when the value is an object;
	// items what is read of each item, when it is a list. A field with
	// neither, nor whole, is only noted as there.
	props map[string]*wanted
	items *wanted
}

// there stands for a field naming needs only to know is there.
type there struct{}

// wantedBy is what the marks of every kind of a side read.
func wantedBy(kinds []kind) *wanted {
	root := &wanted{}
	for _, k := range kinds {
		for _, mark := range k.matches {
			w := root
			for _, name := range mark.path {
				w = w.prop(name)
			}
			w.whole = w.whole || mark.hasValue
		}
		if k.correlation != nil {
			root.pointer(k.correlation)
		}
	}
	return root
}

// pointer adds a JSON pointer's value, kept whole. A step that is a
// number reads that item of a list, or that property of an object.
func (w *wanted) pointer(steps []string) {
	if len(steps) == 0 {
		w.whole = true
		return
	}
	w.prop(steps[0]).pointer(steps[1:])
	if _, err := strconv.Atoi(steps[0]); err == nil {
		if w.items == nil {
			w.items = &wanted{}
		}
		w.items.pointer(steps[1:])
	}
}

func (w *wanted) prop(name string) *wanted {
	if w.props == nil {
		w.props = map[string]*wanted{}
	}
	if w.props[name] == nil {
		w.props[name] = &wanted{}
	}
	return w.props[name]
}

// read reads data as far as it is whole, keeping only what w wants: a
// message cut at the size kept still gives the fields before the cut, so
// it can be named. It reads the data where it is, without a copy.
func (w *wanted) read(data []byte) any {
	d := jsontext.NewDecoder(bytes.NewBuffer(data), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	v, _ := w.value(d)
	return v
}

// value reads the next value, and says whether it was whole.
func (w *wanted) value(d *jsontext.Decoder) (any, bool) {
	kind := d.PeekKind()
	switch {
	case w.whole:
		return wholeValue(d)
	case kind == '{' && w.props != nil:
		// The peek saw the brace, so reading it cannot fail.
		_, _ = d.ReadToken()
		m := map[string]any{}
		for d.PeekKind() != '}' {
			// A name cut short reads as no property of w's, so its value is
			// skipped, which fails as the decoder has failed.
			name, _ := d.ReadToken()
			key := name.String() // before the next read, which reuses its memory
			next, read := w.props[key]
			if !read {
				if d.SkipValue() != nil {
					return m, false
				}
				continue
			}
			v, whole := next.value(d)
			if v != nil || whole {
				m[key] = v
			}
			if !whole {
				return m, false
			}
		}
		_, err := d.ReadToken()
		return m, err == nil
	case kind == '[' && w.items != nil:
		_, _ = d.ReadToken()
		var list []any
		for d.PeekKind() != ']' {
			v, whole := w.items.value(d)
			if v != nil || whole {
				list = append(list, v)
			}
			if !whole {
				return list, false
			}
		}
		_, err := d.ReadToken()
		return list, err == nil
	}
	if d.SkipValue() != nil {
		return nil, false
	}
	return there{}, true
}

// wholeValue reads the next value whole, as plain Go data with its numbers
// as json.Number, as written.
// A value cut short reads as nothing: its raw bytes are empty, so the
// decoding leaves v nil.
func wholeValue(d *jsontext.Decoder) (any, bool) {
	raw, err := d.ReadValue()
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&v)
	return v, err == nil
}

// asyncMessages starts a $ref to a message of components/x-async-api.
const asyncMessages = "#/components/x-async-api/messages/"

// placed is a message as an operation lists it: its kind, the side that
// sends it, and its content type.
type placed struct {
	kind        kind
	fromApp     bool
	contentType string
}

// asyncAPI reads the x-async-api mark of an operation: its operations, and
// the messages they list.
func (b *builder) asyncAPI(v any, pointer, where string) *Messages {
	m, at := b.object(v, pointer, where)
	ops, hasOps := m["operations"].(map[string]any)
	if !hasOps {
		b.problem("%s: x-async-api must hold operations", where)
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if key != "operations" && key != "defaultContentType" {
			b.problem("%s: x-async-api has %s, but holds only operations and defaultContentType", where, key)
		}
	}
	defaultType, set := m["defaultContentType"].(string)
	if !set {
		defaultType = "application/json"
	}
	var all []placed
	operationOf := map[string]string{}
	// place reads the messages one operation lists on one side, each of
	// which may belong to no other operation (rule 3).
	place := func(refs []ref, operation, role string, fromApp bool) {
		for _, r := range refs {
			if other, listed := operationOf[r.name]; listed {
				b.problem("%s: the message %s is in the operations %s and %s: a message belongs to one operation", where, r.name, other, operation)
				continue
			}
			operationOf[r.name] = operation
			if p, ok := b.message(r, operation, role, fromApp, defaultType, where); ok {
				all = append(all, p)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(ops)) {
		opWhere := where + ": the operation " + name
		op, opAt := b.object(ops[name], at+"/operations"+token(name), where)
		var fromApp bool
		switch op["action"] {
		case "receive":
			fromApp = true
		case "send":
		default:
			b.problem("%s: action must be send or receive", opWhere)
			continue
		}
		if _, has := op["traits"]; has {
			b.problem("%s: traits are not read: write what they add into the operation itself", opWhere)
		}
		messages := b.refs(op["messages"], opAt+"/messages", opWhere)
		v, hasReply := op["reply"]
		if !hasReply {
			place(messages, name, rolePush, fromApp)
			continue
		}
		reply, replyAt := b.object(v, opAt+"/reply", where)
		if slices.ContainsFunc(slices.Collect(maps.Keys(reply)), func(key string) bool { return key != "messages" }) {
			b.problem("%s: reply holds only messages: channel and address are not used, as one WebSocket is one channel", opWhere)
		}
		place(messages, name, roleRequest, fromApp)
		place(b.refs(reply["messages"], replyAt+"/messages", opWhere), name, roleReply, !fromApp)
	}
	return &Messages{
		app:    b.side(all, true, where),
		vendor: b.side(all, false, where),
	}
}

// ref is a $ref to a message, by the message's name.
type ref struct {
	name    string
	item    any
	pointer string
}

// refs reads a list of $refs to messages.
func (b *builder) refs(v any, pointer, where string) []ref {
	list, isList := v.([]any)
	if !isList {
		b.problem("%s: messages must be a list of $refs to components/x-async-api/messages", where)
		return nil
	}
	var out []ref
	for i, item := range list {
		name, found := strings.CutPrefix(refOf(item), asyncMessages)
		if !found {
			b.problem("%s: message %d must be a $ref to components/x-async-api/messages", where, i+1)
			continue
		}
		out = append(out, ref{name: name, item: item, pointer: fmt.Sprintf("%s/%d", pointer, i)})
	}
	return out
}

// refOf is the $ref of v, or nothing when v is not a $ref.
func refOf(v any) string {
	m, _ := v.(map[string]any)
	target, _ := m["$ref"].(string)
	return target
}

// message reads one message, sent by the app or the vendor: its payload
// schema's marks and check, its correlation id and its content type. A
// message without a reply of its own that has a correlation id answers a
// request of any operation (Q9).
func (b *builder) message(r ref, operation, role string, fromApp bool, defaultType, where string) (placed, bool) {
	m, at := b.object(r.item, r.pointer, where)
	if m == nil {
		return placed{}, false
	}
	msgWhere := where + ": the message " + r.name
	if _, has := m["traits"]; has {
		b.problem("%s: traits are not read: write what they add into the message itself", msgWhere)
	}
	payload := m["payload"]
	b.schemaFormat(m, msgWhere)
	if p, isObject := payload.(map[string]any); isObject {
		b.schemaFormat(p, msgWhere)
	}
	schemaName, shared := strings.CutPrefix(refOf(payload), sharedPrefix)
	if !shared {
		b.problem("%s: payload must be a $ref to components/schemas", msgWhere)
		return placed{}, false
	}
	schema, schemaAt := b.object(payload, at+"/payload", where)
	if schema == nil {
		return placed{}, false
	}
	k := kind{name: r.name, operation: operation, role: role}
	b.messageMarks(schema, schemaAt, nil, false, map[string]bool{}, &k)
	if len(k.matches) == 0 {
		b.problem("%s: the message %s has no x-hotline-match, so no message can be named %s", where, r.name, r.name)
	}
	k.check = b.messageCheck(payload, schemaAt, schemaName, r.name, where)
	if v, has := m["correlationId"]; has {
		k.correlation = b.correlation(v, at+"/correlationId", msgWhere)
	}
	if k.role == rolePush && k.correlation != nil {
		k.role = roleAnswer
	}
	contentType, set := m["contentType"].(string)
	if !set {
		contentType = defaultType
	}
	return placed{kind: k, fromApp: fromApp, contentType: contentType}, true
}

// correlation reads a correlationId: its location must be a JSON pointer
// into the payload, as a WebSocket message has no headers (rule 5).
func (b *builder) correlation(v any, pointer, where string) []string {
	m, _ := b.object(v, pointer, where)
	location, _ := m["location"].(string)
	inPayload, found := strings.CutPrefix(location, "$message.payload#/")
	switch {
	case strings.HasPrefix(location, "$message.header"):
		b.problem("%s: correlationId.location %s: a WebSocket message has no headers", where, location)
		return nil
	case !found:
		b.problem("%s: correlationId.location must start with $message.payload#/, then a JSON pointer", where)
		return nil
	}
	steps := strings.Split(inPayload, "/")
	for i, s := range steps {
		steps[i] = strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
	}
	return steps
}

// side gathers the messages one side sends. They share one content type,
// so the proxy knows whether it can read the side before it names a
// message.
func (b *builder) side(all []placed, fromApp bool, where string) side {
	var kinds []kind
	var types []string
	for _, p := range all {
		if p.fromApp != fromApp {
			continue
		}
		kinds = append(kinds, p.kind)
		mediaType, _, _ := mime.ParseMediaType(p.contentType)
		if !slices.Contains(types, mediaType) {
			types = append(types, mediaType)
		}
	}
	if len(types) > 1 {
		slices.Sort(types)
		b.problem("%s: the messages the %s sends mix the content types %s", where, sideName(fromApp), strings.Join(types, " and "))
	}
	b.clearMatches(kinds, where)
	return side{readable: len(types) == 0 || isJSON(types[0]), kinds: kinds, wanted: wantedBy(kinds)}
}

// sideName names a side in a problem.
func sideName(fromApp bool) string {
	if fromApp {
		return "app"
	}
	return "vendor"
}

// messageMarks walks a message schema's properties, through objects and
// lists, for its x-hotline-match marks.
func (b *builder) messageMarks(m map[string]any, pointer string, path []string, inList bool, seen map[string]bool, k *kind) {
	if seen[pointer] {
		return
	}
	seen[pointer] = true
	defer delete(seen, pointer)
	props, _ := m["properties"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		propPointer := pointer + "/properties" + token(name)
		raw, _ := props[name].(map[string]any)
		resolved, resolvedAt := b.object(raw, propPointer, b.name)
		if resolved == nil {
			continue
		}
		inner := append(slices.Clone(path), name)
		if mark, marked := raw["x-hotline-match"]; marked {
			switch {
			case mark != true:
				b.problem("%s: x-hotline-match at %s must be true", b.name, propPointer)
			case inList:
				b.problem("%s: x-hotline-match at %s sits inside a list, which has no one value to match", b.name, propPointer)
			default:
				value, hasValue := resolved["const"]
				k.matches = append(k.matches, matchMark{path: inner, value: value, hasValue: hasValue})
			}
		}
		if _, marked := raw["x-hotline-id-tuple"]; marked {
			b.problem("%s: x-hotline-id-tuple is replaced by the message's correlationId (at %s)", b.name, propPointer)
		}
		b.messageMarks(resolved, resolvedAt, inner, inList, seen, k)
		if items, ok := resolved["items"]; ok {
			if item, itemAt := b.object(items, resolvedAt+"/items", b.name); item != nil {
				b.messageMarks(item, itemAt, inner, true, seen, k)
			}
		}
	}
}

// clearMatches refuses two messages of a side that would both match the
// same messages: when every mark of one is also a mark of the other, with
// the same const or none, any message matching the second matches the
// first too (Peter, 2026-11-19, Q3).
func (b *builder) clearMatches(kinds []kind, where string) {
	for i := range kinds {
		for j := i + 1; j < len(kinds); j++ {
			if covers(kinds[i], kinds[j]) || covers(kinds[j], kinds[i]) {
				b.problem("%s: the messages %s and %s both match the same messages: give one a mark the other lacks, or a different const",
					where, kinds[i].name, kinds[j].name)
			}
		}
	}
}

// covers says whether every message that matches other matches k too.
func covers(k, other kind) bool {
	if len(k.matches) == 0 {
		return false
	}
	for _, mine := range k.matches {
		if !slices.ContainsFunc(other.matches, func(theirs matchMark) bool {
			return slices.Equal(mine.path, theirs.path) &&
				(!mine.hasValue || theirs.hasValue && reflect.DeepEqual(mine.value, theirs.value))
		}) {
			return false
		}
	}
	return true
}

// messageCheck builds the check of one message as a response with a JSON
// body of its payload schema, with that schema's x-hotline-record fields.
func (b *builder) messageCheck(item any, pointer, schemaName, name, where string) ResponseCheck {
	ct := content{
		mediaType: "application/json",
		schema:    schemaAt{value: map[string]any{"$ref": sharedPrefix + schemaName}, found: true, pointer: pointer, what: "the message " + name},
		fields:    b.fields(item, pointer),
	}
	return b.responseCheck(&response{body: &bodySpec{content: []content{ct}}}, where)
}

// schemaFormat refuses a schemaFormat, on a message or its payload, that is
// not JSON Schema, such as Avro or Protobuf. The payload would be read as
// JSON Schema all the same and its checks would mean nothing, so the spec
// is refused rather than the key skipped (Peter, 2026-11-21, M1).
func (b *builder) schemaFormat(m map[string]any, where string) {
	v, has := m["schemaFormat"]
	if !has {
		return
	}
	// These start the formats whose schemas are read as JSON Schema:
	// AsyncAPI's own, OpenAPI's, and JSON Schema itself.
	jsonSchemaFormats := []string{"application/vnd.aai.asyncapi", "application/vnd.oai.openapi", "application/schema+"}
	format, _ := v.(string)
	if !slices.ContainsFunc(jsonSchemaFormats, func(prefix string) bool { return strings.HasPrefix(format, prefix) }) {
		b.problem("%s: schemaFormat %v is not JSON Schema", where, v)
	}
}
