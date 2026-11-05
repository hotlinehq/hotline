package spec

import (
	"cmp"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Result is one thing a check found: the part of the call, the place in it,
// the rule it broke and what the spec expects. It never holds a value from
// the call.
type Result struct {
	Part     string
	Place    string
	Rule     string
	Expected string
}

// Body is the copy of a body the proxy kept for the checks.
type Body struct {
	Data []byte
	// TooLarge says the body was longer than the copy.
	TooLarge bool
}

// Exchange is a finished call, as the checks see it.
type Exchange struct {
	Method      string
	EscapedPath string
	RawQuery    string
	Header      http.Header
	Body        Body
	// Action is what the request body names as the action, from
	// BodyAction, for a spec that picks routes by a body field.
	Action string
	// Status is 0 when no reply came.
	Status      int
	ReplyHeader http.Header
	ReplyBody   Body
}

// Checked is what the checks found on each side, and why some checks were
// left out, if any were.
type Checked struct {
	Request []Result
	Reply   []Result
	Skipped string
	// Copied are the marked fields of both bodies.
	Copied []Field
}

// checks are what the spec says about one route: its parameters, who may
// call it, its body and its replies.
type checks struct {
	params []param
	// security lists the ways to log in, any one of which is enough. Nil
	// or a way with no credentials means none is needed.
	security [][]credential
	body     *bodySpec
	// replies are by status: "200", "2XX" or "default". Nil when the spec
	// lists none, so replies are not checked.
	replies map[string]*reply
	// request checks the parameters, login and body against the schemas.
	request RequestCheck
}

type param struct {
	name     string
	in       string
	required bool
	// deepObject reads an object from name[key]=value query pairs.
	deepObject bool
	explode    bool
	schema     schemaAt
	// record names the record attribute a header is copied to, when the
	// spec marks it x-hotline-record.
	record string
}

type credential struct {
	in   string
	name string
}

type bodySpec struct {
	required bool
	content  []content
}

type content struct {
	mediaType string
	// json is set when the spec marks the type x-hotline-body: json, for a
	// vendor's own name for JSON.
	json bool
	// xmlMark is set when the spec marks the type x-hotline-body: xml.
	xmlMark bool
	// xml is how the body is read as XML; nil when it can't be XML.
	xml *xmlBody
	// schema is not found when the spec gives the type but no schema.
	schema schemaAt
	// fields are the body fields marked x-hotline-record.
	fields []field
}

type reply struct {
	headers []param
	body    *bodySpec
	// check checks the headers and body against the schemas.
	check ResponseCheck
}

const (
	partBody   = "body"
	partHeader = "header"
	partStatus = "status"
	whole      = "$"
)

// Check checks a finished call against the spec, both sides. A call with
// no route is not checked.
func (s *Spec) Check(e Exchange) Checked {
	return s.CheckRequest(e).With(s.CheckReply(e))
}

// With joins the check of a request with the check of its reply. When both
// left part out, the request's reason is kept.
func (c Checked) With(reply Checked) Checked {
	c.Reply = reply.Reply
	c.Copied = append(c.Copied, reply.Copied...)
	c.Skipped = cmp.Or(c.Skipped, reply.Skipped)
	return c
}

// CheckRequest checks the request side of a call: its parameters, login
// and body. It can run as soon as the request has been sent (Peter,
// 2026-10-31).
func (s *Spec) CheckRequest(e Exchange) Checked {
	r := s.match(e.Method, e.EscapedPath, e.RawQuery, e.Header, e.Action)
	if r.segments == nil {
		return Checked{Skipped: "no_spec_route"}
	}
	c := Checked{
		Request: r.checks.request.Meta(e.EscapedPath, e.RawQuery, e.Header),
		Copied:  r.checks.copiedHeaders(e.Header),
	}
	results, skipped, mediaType, check := r.checks.body.gate(e.Header.Get("Content-Type"), e.Body)
	c.Request = append(c.Request, results...)
	c.Skipped = skipped
	if check {
		results, copied := r.checks.request.Body(mediaType, e.Body.Data)
		c.Request = append(c.Request, results...)
		c.Copied = append(c.Copied, copied...)
	}
	return c
}

// CheckReply checks the reply side of a call: its status, headers and
// body. It can run as soon as the reply has arrived (Peter, 2026-10-31).
func (s *Spec) CheckReply(e Exchange) Checked {
	r := s.match(e.Method, e.EscapedPath, e.RawQuery, e.Header, e.Action)
	if r.segments == nil {
		return Checked{Skipped: "no_spec_route"}
	}
	var c Checked
	if e.Status == 0 || r.checks.replies == nil {
		return c
	}
	rp := r.checks.reply(e.Status)
	if rp == nil {
		c.Reply = []Result{{Part: partStatus, Place: strconv.Itoa(e.Status), Rule: "not_in_spec"}}
		return c
	}
	c.Reply = rp.check.Meta(e.ReplyHeader)
	if mt, _, _ := mime.ParseMediaType(e.ReplyHeader.Get("Content-Type")); mt == "text/event-stream" {
		c.Skipped = "streaming"
		return c
	}
	results, skipped, mediaType, check := rp.body.gate(e.ReplyHeader.Get("Content-Type"), e.ReplyBody)
	c.Reply = append(c.Reply, results...)
	c.Skipped = skipped
	if check {
		results, copied := rp.check.Body(mediaType, e.ReplyBody.Data)
		c.Reply = append(c.Reply, results...)
		c.Copied = copied
	}
	return c
}

func (c *checks) reply(status int) *reply {
	for _, key := range []string{strconv.Itoa(status), fmt.Sprintf("%dXX", status/100), "default"} {
		if rp, ok := c.replies[key]; ok {
			return rp
		}
	}
	return nil
}

// values are the parameter's values in the call, nil when it is missing.
func (p param) values(r route, segments []string, query url.Values, header http.Header, cookies map[string]string) any {
	switch p.in {
	case "path":
		for i, segment := range r.segments {
			if isValue(segment) && valueName(segment) == p.name {
				// The proxy only forwards paths that unescape.
				value, _ := url.PathUnescape(segments[i])
				return []string{value}
			}
		}
	case "query":
		if p.deepObject {
			return deepObject(p.name, query)
		}
		if values, ok := query[p.name]; ok {
			return values
		}
	case "header":
		if values := header.Values(p.name); len(values) > 0 {
			return []string{strings.Join(values, ", ")}
		}
	case "cookie":
		if value, ok := cookies[p.name]; ok {
			return []string{value}
		}
	}
	return nil
}

// deepObject reads name[key]=value pairs into an object, nil when there
// are none.
func deepObject(name string, query url.Values) any {
	found := url.Values{}
	for key, values := range query {
		if rest, ok := strings.CutPrefix(key, name+"["); ok {
			found["v["+rest] = values
		}
	}
	if len(found) == 0 {
		return nil
	}
	return nest(found)["v"]
}

// unknownQuery reports query names the spec does not know. A login key
// sent in the query, and a word the route needs, are known.
func (s *Spec) unknownQuery(r route, query url.Values) []Result {
	var results []Result
	for _, key := range slices.Sorted(maps.Keys(query)) {
		isWord := slices.ContainsFunc(r.words, func(w queryWord) bool { return w.name == key })
		if !s.secretQuery[key] && !isWord && !knownQuery(r.checks.params, key) {
			results = append(results, Result{Part: "query", Place: key, Rule: "not_in_spec"})
		}
	}
	return results
}

func knownQuery(params []param, key string) bool {
	for _, p := range params {
		if p.in == "query" && (p.name == key || p.deepObject && strings.HasPrefix(key, p.name+"[")) {
			return true
		}
	}
	return false
}

// loggedIn reports the credentials of the first way to log in when no way
// is complete. Only whether a credential is there is checked, never its
// value.
func (c *checks) loggedIn(header http.Header, query url.Values, cookies map[string]string) []Result {
	if len(c.security) == 0 {
		return nil
	}
	var first []Result
	for i, way := range c.security {
		var missing []Result
		for _, cred := range way {
			if !cred.present(header, query, cookies) {
				missing = append(missing, Result{Part: cred.in, Place: cred.name, Rule: "required"})
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if i == 0 {
			first = missing
		}
	}
	return first
}

func (cred credential) present(header http.Header, query url.Values, cookies map[string]string) bool {
	switch cred.in {
	case "query":
		return query.Has(cred.name)
	case "cookie":
		_, ok := cookies[cred.name]
		return ok
	}
	return header.Get(cred.name) != ""
}

// gate makes the checks of a body that need no schema: whether the spec
// lists a body here, whether one is required, and whether its content type
// is one the spec lists. It returns the media type to check the body as,
// and whether to check it. A body that cannot be read,
// or is too large to check, is skipped, and the reason returned.
func (b *bodySpec) gate(contentType string, body Body) (results []Result, skipped, mediaType string, check bool) {
	empty := len(body.Data) == 0 && !body.TooLarge
	switch {
	case b == nil && empty:
		return nil, "", "", false
	case b == nil:
		return []Result{{Part: partBody, Place: whole, Rule: "not_in_spec"}}, "", "", false
	case empty && b.required:
		return []Result{{Part: partBody, Place: whole, Rule: "required"}}, "", "", false
	case empty:
		return nil, "", "", false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = ""
	}
	c, _ := b.find(mediaType)
	if c == nil {
		listed := make([]string, len(b.content))
		for i, c := range b.content {
			listed[i] = c.mediaType
		}
		return []Result{{Part: partBody, Place: whole, Rule: "content_type", Expected: strings.Join(listed, ", ")}}, "", "", false
	}
	switch {
	case c.kind(mediaType) == unreadableBodyKind:
		return nil, "unsupported_content_type", "", false
	case body.TooLarge:
		return nil, "body_too_large", "", false
	}
	return nil, "", mediaType, true
}

// find picks the content for a media type: the exact type first, then
// type/* and */*. It returns the content and its place in the list.
func (b *bodySpec) find(mediaType string) (*content, int) {
	major, _, _ := strings.Cut(mediaType, "/")
	for _, want := range []string{mediaType, major + "/*", "*/*"} {
		for i, c := range b.content {
			if strings.EqualFold(c.mediaType, want) {
				return &b.content[i], i
			}
		}
	}
	return nil, -1
}

// bodyKind is how a body is read before it is checked.
type bodyKind int

const (
	unreadableBodyKind bodyKind = iota
	xmlBodyKind
	jsonBodyKind
	formBodyKind
)

// kind is how a body of this content, sent as mediaType, is read.
func (c *content) kind(mediaType string) bodyKind {
	switch {
	case c.xmlMark || !c.json && isXML(mediaType) && c.xml != nil:
		return xmlBodyKind
	case c.json || isJSON(mediaType):
		return jsonBodyKind
	case mediaType == "application/x-www-form-urlencoded":
		return formBodyKind
	}
	return unreadableBodyKind
}

// readCookies puts the cookies a request sends into cookies, by name. The
// map is the caller's, so it can stay off the heap.
func readCookies(header http.Header, cookies map[string]string) {
	for _, cookie := range (&http.Request{Header: header}).Cookies() {
		cookies[cookie.Name] = cookie.Value
	}
}

func isJSON(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// nest reads bracketed names, as Stripe writes them, into objects and
// lists: items[0][price]=p1 is {"items": [{"price": "p1"}]}. A name with no
// brackets keeps its first value.
func nest(values url.Values) map[string]any {
	root := map[string]any{}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		path := splitBrackets(key)
		for _, value := range values[key] {
			put(root, path, value)
		}
	}
	return lists(root).(map[string]any)
}

func splitBrackets(key string) []string {
	name, rest, found := strings.Cut(key, "[")
	path := []string{name}
	for found {
		var part string
		part, rest, _ = strings.Cut(rest, "]")
		path = append(path, part)
		_, rest, found = strings.Cut(rest, "[")
	}
	return path
}

func put(m map[string]any, path []string, value string) {
	key := path[0]
	if key == "" {
		key = strconv.Itoa(len(m))
	}
	if len(path) == 1 {
		if _, set := m[key]; !set {
			m[key] = value
		}
		return
	}
	child, ok := m[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[key] = child
	}
	put(child, path[1:], value)
}

// lists turns objects whose names are all numbers into lists, in the
// order of the numbers.
func lists(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for key, child := range m {
		m[key] = lists(child)
	}
	indexes := make([]int, 0, len(m))
	for key := range m {
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || strconv.Itoa(i) != key {
			return m
		}
		indexes = append(indexes, i)
	}
	if len(indexes) == 0 {
		return m
	}
	sort.Ints(indexes)
	list := make([]any, len(indexes))
	for i, index := range indexes {
		list[i] = m[strconv.Itoa(index)]
	}
	return list
}
