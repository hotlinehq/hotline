package spec

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"mime"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Messages are the messages each side of a WebSocket may send, as the
// operation that answers 101 lists them under x-hotline-messages (Peter,
// 2026-11-19, WebSocket slice 2, Q25):
//
//	x-hotline-messages:
//	  fromApp:    [{ $ref: '#/components/schemas/toolResponse' }]
//	  fromVendor: [{ $ref: '#/components/schemas/toolCall' }]
//
// A side may also be written { contentType: ..., messages: [...] }, for a
// side whose messages are not JSON. A message is named by the fields its
// schema marks x-hotline-match, and checked against that schema with the
// same checker as call bodies. It never changes after Parse.
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

// kind is one message schema: its name in components/schemas, the marks
// that find it, the ids it holds and its check.
type kind struct {
	name    string
	matches []matchMark
	ids     []idMark
	check   ResponseCheck
}

// matchMark is a field marked x-hotline-match: it must be there and, when
// its schema has a const, have that value.
type matchMark struct {
	path     []string
	value    any
	hasValue bool
}

// idMark is a field marked x-hotline-id-tuple: its value is listed on the
// message's record as label:value. Its path may go through lists, where
// each item is read.
type idMark struct {
	path  []step
	label string
}

// step is one step of an id's path: a property, or every item of a list.
type step struct {
	name string
	each bool
}

// Message is one message, as the step that names it found it. That step
// is never dropped, so its name and ids are known even when the message's
// check is skipped (Peter, 2026-11-19, Q14).
type Message struct {
	// Name is the message schema's name, or unknown when no schema's marks
	// all match.
	Name string
	// IDs are the marked ids, as label:value, in the order the marks and
	// the message list them.
	IDs []string
	// Skipped says why the message can't be checked at all, such as
	// unsupported_content_type.
	Skipped string
	kind    *kind
}

// Unknown names a message no schema's marks match.
const Unknown = "unknown"

// Messages are the messages of the route's WebSocket; nil when the route
// lists none, so the session is counted in bytes only.
func (f Route) Messages() *Messages {
	if f.r.checks == nil {
		return nil
	}
	return f.r.checks.messages
}

// Name finds which message data is: the first listed schema of the side
// whose marked fields all match. A message cut at the size kept is named
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
			return Message{Name: k.name, IDs: k.idsIn(v), kind: k}
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

// idsIn lists the message's marked ids that are text, as label:value.
func (k *kind) idsIn(v any) []string {
	var ids []string
	for _, mark := range k.ids {
		for _, value := range every(v, mark.path) {
			if text, ok := value.(string); ok {
				ids = append(ids, mark.label+":"+text)
			}
		}
	}
	return ids
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

// every is each value at a path that may go through lists.
func every(v any, path []step) []any {
	if len(path) == 0 {
		return []any{v}
	}
	var out []any
	switch first := path[0]; {
	case first.each:
		list, _ := v.([]any)
		for _, item := range list {
			out = append(out, every(item, path[1:])...)
		}
	default:
		if m, ok := v.(map[string]any); ok {
			if child, found := m[first.name]; found {
				out = every(child, path[1:])
			}
		}
	}
	return out
}

// wanted is the part of a side's messages that naming reads: the fields
// its marks name, and nothing else. A Gemini Live message is mostly
// audio no mark looks at, and reading all of it, then reading it again
// for the check, handed out over half of a session's memory (load test,
// 2026-11-20).
type wanted struct {
	// whole keeps the value here as it is: a const to compare, or an id.
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
		for _, mark := range k.ids {
			w := root
			for _, st := range mark.path {
				if st.each {
					if w.items == nil {
						w.items = &wanted{}
					}
					w = w.items
					continue
				}
				w = w.prop(st.name)
			}
			w.whole = true
		}
	}
	return root
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

// idLabel is what an x-hotline-id-tuple label may hold.
var idLabel = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// messages reads the x-hotline-messages mark of an operation.
func (b *builder) messages(v any, pointer, where string) *Messages {
	m, ok := v.(map[string]any)
	if !ok {
		b.problem("%s: x-hotline-messages must list the messages fromApp and fromVendor", where)
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		if key != "fromApp" && key != "fromVendor" {
			b.problem("%s: x-hotline-messages has %s, but lists only fromApp and fromVendor", where, key)
		}
	}
	return &Messages{
		app:    b.side(m["fromApp"], pointer+"/fromApp", where+" fromApp"),
		vendor: b.side(m["fromVendor"], pointer+"/fromVendor", where+" fromVendor"),
	}
}

// side reads one side's list of message schemas.
func (b *builder) side(v any, pointer, where string) side {
	contentType := "application/json"
	list, isList := v.([]any)
	if m, ok := v.(map[string]any); ok {
		if ct, set := m["contentType"].(string); set {
			contentType = ct
		}
		list, isList = m["messages"].([]any)
		pointer += "/messages"
	}
	if v != nil && !isList {
		b.problem("%s: the messages must be a list of $refs to components/schemas", where)
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	s := side{readable: isJSON(mediaType)}
	for i, item := range list {
		ref, _ := item.(map[string]any)["$ref"].(string)
		name, shared := strings.CutPrefix(ref, sharedPrefix)
		if !shared {
			b.problem("%s: message %d must be a $ref to components/schemas", where, i+1)
			continue
		}
		schema, at := b.object(item, fmt.Sprintf("%s/%d", pointer, i), where)
		if schema == nil {
			continue
		}
		k := kind{name: name}
		b.messageMarks(schema, at, nil, false, map[string]bool{}, &k)
		if len(k.matches) == 0 {
			b.problem("%s: the message %s has no x-hotline-match, so no message can be named %s", where, name, name)
		}
		k.check = b.messageCheck(item, at, name, where)
		s.kinds = append(s.kinds, k)
	}
	b.clearMatches(s.kinds, where)
	s.wanted = wantedBy(s.kinds)
	return s
}

// messageMarks walks a message schema's properties, through objects and
// lists, for its x-hotline-match and x-hotline-id-tuple marks.
func (b *builder) messageMarks(m map[string]any, pointer string, path []step, inList bool, seen map[string]bool, k *kind) {
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
		inner := append(slices.Clone(path), step{name: name})
		if mark, marked := raw["x-hotline-match"]; marked {
			switch {
			case mark != true:
				b.problem("%s: x-hotline-match at %s must be true", b.name, propPointer)
			case inList:
				b.problem("%s: x-hotline-match at %s sits inside a list, which has no one value to match", b.name, propPointer)
			default:
				value, hasValue := resolved["const"]
				names := make([]string, len(inner))
				for i, s := range inner {
					names[i] = s.name
				}
				k.matches = append(k.matches, matchMark{path: names, value: value, hasValue: hasValue})
			}
		}
		if mark, marked := raw["x-hotline-id-tuple"]; marked {
			label, isText := mark.(string)
			if !isText || !idLabel.MatchString(label) {
				b.problem("%s: x-hotline-id-tuple at %s must be a label, such as tool-call", b.name, propPointer)
			} else {
				k.ids = append(k.ids, idMark{path: inner, label: label})
			}
		}
		b.messageMarks(resolved, resolvedAt, inner, inList, seen, k)
		if items, ok := resolved["items"]; ok {
			if item, itemAt := b.object(items, resolvedAt+"/items", b.name); item != nil {
				b.messageMarks(item, itemAt, append(inner, step{each: true}), true, seen, k)
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

// messageCheck builds the check of one message schema as a response with
// a JSON body of that schema, with its x-hotline-record fields.
func (b *builder) messageCheck(item any, pointer, name, where string) ResponseCheck {
	ct := content{
		mediaType: "application/json",
		schema:    schemaAt{value: map[string]any{"$ref": sharedPrefix + name}, found: true, pointer: pointer, what: "the message " + name},
		fields:    b.fields(item, pointer),
	}
	return b.responseCheck(&response{body: &bodySpec{content: []content{ct}}}, where)
}
