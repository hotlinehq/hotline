package spec

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Call is what the proxy knows about a call when it builds the record.
type Call struct {
	// Origin is the vendor address up to the path, such as
	// https://api.talon.one or http://api.x.com:8080.
	Origin       string
	EscapedPath  string
	RawQuery     string
	Header       http.Header
	ReplyHeaders http.Header
}

// Route is the spec's route for one call, found once by Find and handed to
// the checks and the redaction, so none of them looks it up again. The zero
// Route is no route.
type Route struct {
	r route
}

// Find finds the route for a call from its method, path, query and
// headers, and for a spec that picks routes by a body field, the action
// the request body names (Exchange.Action).
func (s *Spec) Find(e Exchange) Route {
	return Route{r: s.match(e.Method, e.EscapedPath, e.RawQuery, e.Header, e.Action)}
}

// Redacted is the part of a record that the spec decides. URLFull and
// Template are empty when the spec has no route for the call: without a
// route, the proxy cannot tell values from fixed parts of the path.
type Redacted struct {
	URLFull      string
	Template     string
	Header       map[string][]string
	ReplyHeaders map[string][]string
}

const redacted = "REDACTED"

// Redact applies the marks of the call's route, and the spec's secrets, to
// a call. Path and query values are redacted unless marked
// x-hotline-redact false. Headers are kept only when on the default list or
// marked; secrets never are.
func (s *Spec) Redact(found Route, c Call) Redacted {
	r := found.r
	redacted := Redacted{
		Header:       s.headers(c.Header, r.marks["header"]),
		ReplyHeaders: s.headers(c.ReplyHeaders, r.marks["reply"]),
	}
	if r.segments == nil {
		return redacted
	}
	// Path values are redacted unless marked false.
	redacted.URLFull = c.Origin + r.replacePath(c.EscapedPath, func(name string) bool {
		return r.marks["path"][name] != keep
	})
	if c.RawQuery != "" {
		redacted.URLFull += "?" + s.redactQuery(r, c.RawQuery)
	}
	redacted.Template = r.template
	return redacted
}

// Hide returns the call as content: full writes it: every header and the
// whole address kept, but each value that is secret replaced by REDACTED.
// Secret, always: Authorization, Proxy-Authorization, Cookie and
// Set-Cookie. With a spec, also what its security section names as the
// login (a header or a query word) and every value marked
// x-hotline-redact: true. s is nil for a call with no spec.
func Hide(s *Spec, found Route, c Call) Call {
	r := found.r
	c.Header = hideHeaders(s, c.Header, r.marks["header"])
	c.ReplyHeaders = hideHeaders(s, c.ReplyHeaders, r.marks["reply"])
	if s == nil {
		return c
	}
	// Only what is marked true is hidden here; the record hides more.
	if r.segments != nil {
		c.EscapedPath = r.replacePath(c.EscapedPath, func(name string) bool {
			return r.marks["path"][name] == redact
		})
	}
	c.RawQuery = replaceQuery(c.RawQuery, func(name, _ string) bool {
		return s.secretQuery[name] || r.marks["query"][name] == redact
	})
	return c
}

// hideHeaders copies the headers, with each secret value REDACTED. Unlike
// the record's headers, nothing is left out.
func hideHeaders(s *Spec, h http.Header, marks map[string]mark) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for name, values := range h {
		lower := strings.ToLower(name)
		if neverRecorded(lower) || marks[lower] == redact || (s != nil && s.secretHeaders[lower]) {
			values = []string{redacted}
		}
		out[name] = values
	}
	return out
}

// match finds the route for a call. A fixed segment wins over a templated
// one, which wins over one that takes the rest of the path, and a trailing
// slash makes no difference. A route named by the action header matches
// only when the header has exactly its value, one named by a body field
// only when bodyAction, read from the body, is exactly its value, and one
// named by query words only when the query has each of them, with the
// value given. Each wins over the same path without them. With no route, it returns a route with
// no segments and no marks.
func (s *Spec) match(method, escapedPath, rawQuery string, header http.Header, bodyAction string) route {
	segments := strings.Split(strings.TrimSuffix(escapedPath, "/"), "/")
	action := ""
	switch {
	case s.actionHeader != "":
		action = header.Get(s.actionHeader)
	case s.actionField != "":
		action = bodyAction
	}
	query, _ := url.ParseQuery(rawQuery)
	best := route{marks: map[string]map[string]mark{}}
	for _, r := range s.routes {
		if r.method != method || !r.fits(segments) || r.action != "" && r.action != action || !r.hasWords(query) {
			continue
		}
		if r.beats(best) {
			best = r
		}
	}
	return best
}

func (r route) fits(segments []string) bool {
	if len(segments) < len(r.segments) || !r.rest && len(segments) != len(r.segments) {
		return false
	}
	for i, want := range r.segments {
		if !isValue(want) && want != segments[i] || isValue(want) && segments[i] == "" {
			return false
		}
	}
	return true
}

// hasWords says whether a query holds every word the route needs.
func (r route) hasWords(query url.Values) bool {
	for _, w := range r.words {
		values, ok := query[w.name]
		if !ok || w.hasValue && !slices.Contains(values, w.value) {
			return false
		}
	}
	return true
}

// beats says whether r is more specific than other: at the first segment
// where they differ, r's is fixed and other's a value, or r's a value and
// other's the rest of the path. With the same segments, the route that
// needs more of the call (an action header, query words) wins.
func (r route) beats(other route) bool {
	for i := 0; i < len(r.segments) && i < len(other.segments); i++ {
		if mine, theirs := r.rank(i), other.rank(i); mine != theirs {
			return mine > theirs
		}
	}
	return r.needs() >= other.needs()
}

// rank says how fixed a segment of the route is: 2 for fixed text, 1 for
// a value, 0 for the rest of the path.
func (r route) rank(i int) int {
	switch {
	case !isValue(r.segments[i]):
		return 2
	case r.rest && i == len(r.segments)-1:
		return 0
	}
	return 1
}

// needs counts what a call must have, beyond its path, to take the route.
func (r route) needs() int {
	n := len(r.words)
	if r.action != "" {
		n++
	}
	return n
}

// split cuts a path into the route's segments, the rest of the path being
// one segment for a route that takes it.
func (r route) split(path string) []string {
	segments := strings.Split(path, "/")
	if last := len(r.segments) - 1; r.rest && len(segments) > last {
		segments = append(segments[:last], strings.Join(segments[last:], "/"))
	}
	return segments
}

// replacePath writes REDACTED for each path value of the route whose name
// hide picks, keeping fixed segments and the trailing slash as they are.
func (r route) replacePath(escapedPath string, hide func(name string) bool) string {
	path := strings.TrimSuffix(escapedPath, "/")
	segments := r.split(path)
	for i := range segments {
		if template := r.segments[i]; isValue(template) && hide(valueName(template)) {
			segments[i] = redacted
		}
	}
	return strings.Join(segments, "/") + strings.TrimPrefix(escapedPath, path)
}

// valueName is the name of a path value written {name} or {name+}.
func valueName(segment string) string {
	return strings.TrimSuffix(strings.Trim(segment, "{}"), "+")
}

func isValue(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}

func (s *Spec) redactQuery(r route, rawQuery string) string {
	// A name that does not unescape matches no mark, so it is redacted,
	// and a word's value fixed by the route is no secret.
	return replaceQuery(rawQuery, func(name, value string) bool {
		return !r.fixedWord(name, value) && (s.secretQuery[name] || r.marks["query"][name] != keep)
	})
}

// replaceQuery writes REDACTED for the value of each query word hide picks,
// by its unescaped name and its value as written. An empty value has
// nothing to hide, so it stays empty.
func replaceQuery(rawQuery string, hide func(name, value string) bool) string {
	if rawQuery == "" {
		return ""
	}
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		name, value, _ := strings.Cut(pair, "=")
		plain, _ := url.QueryUnescape(name)
		if value != "" && hide(plain, value) {
			pairs[i] = name + "=" + redacted
		}
	}
	return strings.Join(pairs, "&")
}

// fixedWord says whether the route needs this query word with exactly
// this value, as the value escaped in the call.
func (r route) fixedWord(name, escaped string) bool {
	value, err := url.QueryUnescape(escaped)
	return err == nil && slices.ContainsFunc(r.words, func(w queryWord) bool {
		return w.hasValue && w.name == name && w.value == value
	})
}

func (s *Spec) headers(h http.Header, marks map[string]mark) map[string][]string {
	kept := map[string][]string{}
	for name, values := range h {
		name = strings.ToLower(name)
		switch {
		case neverRecorded(name) || s.secretHeaders[name]:
		case marks[name] == redact:
			kept[name] = []string{redacted}
		case marks[name] == keep || onDefaultList(name):
			kept[name] = values
		}
	}
	return kept
}

func neverRecorded(name string) bool {
	switch name {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}
	return false
}

func onDefaultList(name string) bool {
	switch name {
	case "content-type", "content-length", "date", "retry-after", "request-id", "x-request-id":
		return true
	}
	return strings.HasPrefix(name, "ratelimit-")
}
