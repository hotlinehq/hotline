package spec

import (
	"cmp"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"hotline-proxy/internal/spec/read"
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
	xml *read.XMLBody
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
	found := s.Find(e)
	return s.CheckRequest(found, e).With(s.CheckReply(found, e))
}

// With joins the check of a request with the check of its reply. When both
// left part out, the request's reason is kept.
func (c Checked) With(reply Checked) Checked {
	c.Reply = reply.Reply
	c.Copied = append(c.Copied, reply.Copied...)
	c.Skipped = cmp.Or(c.Skipped, reply.Skipped)
	return c
}

// CheckRequest checks the request side of a call, on the route Find found:
// its parameters, login and body. It can run as soon as the request has
// been sent (Peter, 2026-10-31).
func (s *Spec) CheckRequest(found Route, e Exchange) Checked {
	r := found.r
	if r.segments == nil {
		return Checked{Skipped: "no_spec_route"}
	}
	c := Checked{
		Request: r.checks.request.Meta(e.EscapedPath, e.RawQuery, e.Header),
		Copied:  r.checks.copiedHeaders(e.Header),
	}
	query, _ := url.ParseQuery(e.RawQuery)
	c.Request = append(c.Request, s.unknownQuery(r, query)...)
	cookies := map[string]string{}
	read.Cookies(e.Header, cookies)
	c.Request = append(c.Request, r.checks.loggedIn(e.Header, query, cookies)...)
	results, skipped, content, reads := r.checks.body.gate(e.Header.Get("Content-Type"), e.Body)
	c.Request = append(c.Request, results...)
	c.Skipped = skipped
	if reads != 0 {
		results, copied := r.checks.request.Body(content, reads, e.Body.Data)
		c.Request = append(c.Request, results...)
		c.Copied = append(c.Copied, copied...)
	}
	return c
}

// CheckReply checks the reply side of a call, on the route Find found: its
// status, headers and body. It can run as soon as the reply has arrived
// (Peter, 2026-10-31).
func (s *Spec) CheckReply(found Route, e Exchange) Checked {
	r := found.r
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
	results, skipped, content, reads := rp.body.gate(e.ReplyHeader.Get("Content-Type"), e.ReplyBody)
	c.Reply = append(c.Reply, results...)
	c.Skipped = skipped
	if reads != 0 {
		results, copied := rp.check.Body(content, reads, e.ReplyBody.Data)
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
// is one the spec lists. It returns the place of the content to check the
// body as and how to read it, 0 when the body is not checked. A body that
// cannot be read, or is too large to check, is skipped, and the reason
// returned.
func (b *bodySpec) gate(contentType string, body Body) (results []Result, skipped string, content int, reads Reads) {
	empty := len(body.Data) == 0 && !body.TooLarge
	switch {
	case b == nil && empty:
		return nil, "", 0, 0
	case b == nil:
		return []Result{{Part: partBody, Place: whole, Rule: "not_in_spec"}}, "", 0, 0
	case empty && b.required:
		return []Result{{Part: partBody, Place: whole, Rule: "required"}}, "", 0, 0
	case empty:
		return nil, "", 0, 0
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = ""
	}
	c, i := b.find(mediaType)
	if c == nil {
		listed := make([]string, len(b.content))
		for i, c := range b.content {
			listed[i] = c.mediaType
		}
		return []Result{{Part: partBody, Place: whole, Rule: "content_type", Expected: strings.Join(listed, ", ")}}, "", 0, 0
	}
	reads = c.reads(mediaType)
	switch {
	case reads == 0:
		return nil, "unsupported_content_type", 0, 0
	case body.TooLarge:
		return nil, "body_too_large", 0, 0
	}
	return nil, "", i, reads
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

// reads is how a body of this content, sent as mediaType, is read; 0
// when it can't be.
func (c *content) reads(mediaType string) Reads {
	switch {
	case c.xmlMark || !c.json && read.IsXML(mediaType) && c.xml != nil:
		return ReadsXML
	case c.json || isJSON(mediaType):
		return ReadsJSON
	case mediaType == "application/x-www-form-urlencoded":
		return ReadsForm
	}
	return 0
}

func isJSON(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
