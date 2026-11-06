package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Field is one body field the spec marks x-hotline-record, copied from a
// call or a reply: the record attribute's name, and the value as a string,
// bool, int64 or float64.
type Field struct {
	Name  string
	Value any
}

// field is where a marked field is in a body, and what it is copied as.
type field struct {
	path  []string
	name  string
	types []string
}

// longestText is how many characters of a copied text a record holds.
const longestText = 256

// recordName is what a copied field's name may hold.
var recordName = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// ownNames start the names of the attributes the proxy writes itself, so a
// copied field can never pass for one of them.
func ownNames() []string {
	return []string{"hotline.", "http.", "url.", "server.", "network.", "error.", "dns.", "tls.", "log."}
}

// fields finds the marked fields of a body schema. Only properties are
// followed, through objects; a field inside a list has no one value, so
// it is never copied, but its mark is still checked.
func (b *builder) fields(v any, pointer string) []field {
	var found []field
	b.walk(v, pointer, nil, false, map[string]bool{}, &found)
	return found
}

func (b *builder) walk(v any, pointer string, path []string, inList bool, seen map[string]bool, found *[]field) {
	m, at := b.object(v, pointer, b.name)
	if m == nil || seen[at] {
		return
	}
	seen[at] = true
	defer delete(seen, at)
	props, _ := m["properties"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(props)) {
		raw, _ := props[name].(map[string]any)
		propPointer := at + "/properties" + token(name)
		resolved, resolvedAt := b.object(raw, propPointer, b.name)
		inner := append(slices.Clone(path), name)
		if mark, marked := raw["x-hotline-record"]; marked {
			f, ok := b.mark(mark, kinds(raw, resolved), propPointer)
			if ok && !inList {
				f.path = inner
				*found = append(*found, f)
			}
		}
		b.walk(raw, propPointer, inner, inList, seen, found)
		if items, ok := resolved["items"]; ok {
			b.walk(items, resolvedAt+"/items", inner, true, seen, found)
		}
	}
}

// mark reads one x-hotline-record mark on a body field. A problem with it
// is reported once, however many bodies share the schema.
func (b *builder) mark(mark any, types []string, pointer string) (field, bool) {
	name, problem := recordedAs(mark)
	if problem == "" && (len(types) == 0 || slices.ContainsFunc(types, func(t string) bool {
		return !slices.Contains([]string{"string", "number", "integer", "boolean"}, t)
	})) {
		problem = "must be on a field of text, a number, or true or false, with its type given"
	}
	if problem == "" {
		return field{name: name, types: types}, true
	}
	if !b.reported[pointer] {
		b.reported[pointer] = true
		b.problem("%s: x-hotline-record at %s %s", b.name, pointer, problem)
	}
	return field{}, false
}

// headerMark reads one x-hotline-record mark on a parameter: only a header
// of the call, never a login header, can be copied. It returns the name
// the header is copied to, empty when the mark can't be used.
func (b *builder) headerMark(mark any, in, header, where string) string {
	name, problem := recordedAs(mark)
	switch {
	case in != partHeader:
		problem = "a mark goes only on a header of the call"
	case problem == "" && b.login(header):
		problem = "is on a login header, which is never recorded"
	}
	if problem != "" {
		b.problem("%s: x-hotline-record on the %s parameter %s %s", where, in, header, problem)
		return ""
	}
	return name
}

// login says whether a header carries a login: Authorization, the
// proxy's own, a cookie, or one the spec names in its security section.
func (b *builder) login(header string) bool {
	switch strings.ToLower(header) {
	case "authorization", "proxy-authorization", "cookie":
		return true
	}
	for _, cred := range b.schemes {
		if cred.in == partHeader && strings.EqualFold(cred.name, header) {
			return true
		}
	}
	return false
}

// recordedAs checks the name a mark gives, and says what is wrong with it.
func recordedAs(mark any) (string, string) {
	name, isText := mark.(string)
	switch {
	case !isText:
		return "", "must be a name, such as messaging.destination.name"
	case !recordName.MatchString(name):
		return "", fmt.Sprintf("names %q, but a name may only hold letters, digits, dots and underscores", name)
	case slices.ContainsFunc(ownNames(), func(own string) bool { return strings.HasPrefix(name, own) }):
		return "", fmt.Sprintf("names %s, which starts like the proxy's own names (%s)", name, strings.Join(ownNames(), " "))
	}
	return name, ""
}

// kinds are the types a property allows, null left out: its own, or those
// of the schema its $ref points to.
func kinds(raw, resolved map[string]any) []string {
	t, ok := raw["type"]
	if !ok {
		t = resolved["type"]
	}
	var list []string
	switch x := t.(type) {
	case string:
		list = []string{x}
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				list = append(list, s)
			}
		}
	}
	return slices.DeleteFunc(list, func(s string) bool { return s == "null" })
}

// clashes reports a name marked twice on one route: on a header and in the
// call's body, or in both the call and a reply, since a record holds each
// name once.
func (b *builder) clashes(c *checks, where string) {
	headers := map[string]bool{}
	for _, p := range c.params {
		if p.record != "" {
			headers[p.record] = true
		}
	}
	asked := maps.Clone(headers)
	for _, name := range c.body.names() {
		if headers[name] {
			b.problem("%s: x-hotline-record names %s in both a header and the body of the call", where, name)
		}
		asked[name] = true
	}
	clashing := map[string]bool{}
	for _, rp := range c.replies {
		for _, name := range rp.body.names() {
			if asked[name] {
				clashing[name] = true
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(clashing)) {
		b.problem("%s: x-hotline-record names %s in both the call and the reply", where, name)
	}
}

// names are the names a body's marked fields are copied to.
func (b *bodySpec) names() []string {
	if b == nil {
		return nil
	}
	var names []string
	for _, ct := range b.content {
		for _, f := range ct.fields {
			names = append(names, f.name)
		}
	}
	return names
}

// copiedHeaders are the marked headers of the call, as text. A header sent
// more than once is joined with ", ".
func (c *checks) copiedHeaders(header http.Header) []Field {
	var out []Field
	for _, p := range c.params {
		if values := header.Values(p.name); p.record != "" && len(values) > 0 {
			out = append(out, Field{Name: p.record, Value: cut(strings.Join(values, ", "))})
		}
	}
	return out
}

// copied are the marked fields found in a body read into Go values, each
// with the value it has, if that value is of the marked type.
func (c *content) copied(instance any) []Field {
	values := make([]any, len(c.fields))
	for i, f := range c.fields {
		v := instance
		for _, step := range f.path {
			m, _ := v.(map[string]any)
			v = m[step]
		}
		values[i] = v
	}
	return c.fieldsOf(values)
}

// fieldsOf are the marked fields, given the value found at each one's
// place, in order: those whose value is of the marked type.
func (c *content) fieldsOf(values []any) []Field {
	var out []Field
	for i, f := range c.fields {
		if i >= len(values) {
			break
		}
		if value, ok := plain(values[i], f.types); ok {
			out = append(out, Field{Name: f.name, Value: value})
		}
	}
	return out
}

func plain(v any, types []string) (any, bool) {
	switch x := v.(type) {
	case string:
		if !slices.Contains(types, "string") {
			return nil, false
		}
		return cut(x), true
	case bool:
		return x, slices.Contains(types, "boolean")
	case json.Number:
		if !slices.Contains(types, "integer") && !slices.Contains(types, "number") {
			return nil, false
		}
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(string(x), 64)
		return f, err == nil && !math.IsInf(f, 0)
	}
	return nil, false
}

// cut keeps the first longestText characters of a text.
func cut(text string) string {
	if runes := []rune(text); len(runes) > longestText {
		return string(runes[:longestText])
	}
	return text
}
