package spec

import (
	"net/http"

	"hotline-proxy/internal/spec/read"
)

// The contract between spec and a JSON Schema engine (Peter, 2026-11-06).
// spec reads and reasons about OpenAPI: routes, parameters, content types,
// $ref, x-hotline marks. An engine sees only JSON Schema, and builds the
// checks of each route from a plain description of it. spec keeps those
// checks in its route table and picks one for each call.

// Schema is one JSON Schema 2020-12, as Go's json package decodes an
// object, with numbers as json.Number. Its $ref only ever names a shared
// schema, as "#/$defs/<name>", or a place inside one. spec writes the
// schema true as {} and false as {"not": {}}.
type Schema = map[string]any

// BranchNote is the one key outside JSON Schema that an engine must read.
// The other OpenAPI keys spec leaves in, such as discriminator, xml and
// x-hotline-record, are unknown keywords, which an engine ignores. spec
// writes it, on a schema with oneOf or anyOf, from what the spec's OpenAPI
// discriminator says (Peter, 2026-11-22, M3, Q2), and drops any a spec's
// author wrote. Its value is an object:
//
//	{"property": "type", "choice": "oneOf",
//	 "values": {"text": 0, "image": 1}, "default": 1}
//
// property is the field whose value picks the branch. choice says which
// list the branches are counted in, "oneOf" or "anyOf". values maps each
// value of the field to its branch's position in that list, counted from
// 0, and default, when there is one, is the branch for a missing field or
// a value with no branch of its own. Each position is a json.Number, like
// every number in a Schema. The note never changes pass or fail, as
// OpenAPI says of a discriminator: when the choice fails, an engine
// reports the problems of the branch the note picks, or one problem with
// rule "discriminator" at the field when it picks none.
const BranchNote = "x-hotline-branch"

// NewEngine makes the engine of one spec's schemas. defs are the shared
// schemas by name, which the schemas given later name in $ref. It compiles
// them all, so a broken one is found even when no route uses it. It is
// called once, when the spec is loaded.
type NewEngine func(defs map[string]Schema) (Engine, error)

// Engine builds the checks of each route. It is used while the spec is
// loaded, by one goroutine. An error is a problem with a schema, and the
// spec is refused.
type Engine interface {
	// Request builds the check of one route's request side.
	Request(r RequestSpec) (RequestCheck, error)
	// Response builds the check of one response the spec lists.
	Response(r ResponseSpec) (ResponseCheck, error)
}

// RequestCheck checks the request side of one route. It is safe to use
// from many goroutines at once.
type RequestCheck interface {
	// Params checks the parameters: each one there when required, and its
	// value against its schema. It gets the request as Go's http server
	// gives it, unparsed, but with the proxy's own query names (a login
	// key, a word that picks the route) taken out of the query unless a
	// query parameter has the name.
	Params(escapedPath, rawQuery string, header http.Header) []Finding
	// Body checks a body and copies its marked fields. content is the
	// place in the route's Body list of the content it was sent as, and
	// reads how to read it; spec has found both, and the body is not empty
	// and small enough to check.
	Body(content int, reads Reads, body []byte) ([]Finding, []Field)
}

// ResponseCheck checks one response the spec lists, the way RequestCheck
// checks a request.
type ResponseCheck interface {
	// Headers checks the response's headers.
	Headers(header http.Header) []Finding
	// Body checks a response body, as RequestCheck.Body does.
	Body(content int, reads Reads, body []byte) ([]Finding, []Field)
}

// Reads is how a body is read before it is checked.
type Reads int

const (
	// ReadsJSON reads a JSON body.
	ReadsJSON Reads = iota + 1
	// ReadsXML reads an XML body by the content's XML shape.
	ReadsXML
	// ReadsForm reads a form, with Stripe's brackets nested.
	ReadsForm
	// ReadsText reads percent-decoded text as one string. Only a whole
	// query string is read this way.
	ReadsText
)

// RequestSpec is what the spec says about one route's request.
type RequestSpec struct {
	Params []Param
	// Body holds each content the request body may be sent as, in the
	// order RequestCheck.Body numbers them. Empty when the spec lists no
	// body.
	Body []Content
}

// ResponseSpec is what the spec says about one response.
type ResponseSpec struct {
	// Headers are the response's headers, each with In "header".
	Headers []Param
	Body    []Content
}

// Param is one parameter of a route, or a header of a response.
type Param struct {
	Name string
	// In is path, query, header or cookie. It is also the Part of the
	// findings about it.
	In       string
	Required bool
	// Segment is, for a path value, which piece of the path split at "/"
	// holds it, -1 when the path has none. Rest says the value takes the
	// rest of the path from there.
	Segment int
	Rest    bool
	// Explode is the spec's explode, as spec worked it out; the engine
	// reads ItemSeparator, Spread and ObjectWrittenNameEqualsValue
	// instead.
	Explode bool
	// DeepObject says a query object is written filter[status]=x.
	DeepObject bool
	// WholeQuery, when not 0, says the parameter is the whole query
	// string, read this way: a form, or JSON or text percent-decoded as a
	// form is, with + a space. In is then query, the places inside it read
	// like query parameters (limit, not $.limit), and the whole value's
	// place is $ (Peter, 2026-11-23, M3 part 2, Q1).
	WholeQuery Reads
	// ItemSeparator is the text between the items inside one value: a
	// comma (tag=a,b), a space, |, ; or a dot. An object's names and
	// values are joined by it too. Empty when the value is one item, as
	// in a query list written tag=a&tag=b.
	ItemSeparator string
	// ValuePrefix is the text a path value starts with, taken off before
	// it is read, such as ;id= in ;id=5 or . in .5. Empty when there is
	// none. A value that does not start with it is a finding.
	ValuePrefix string
	// ItemPrefix is the text each item, or the one value, starts with,
	// taken off before it is read, such as id= in ;id=3;id=4 once
	// ValuePrefix ; is taken off. Not used for an object. An item that
	// does not start with it is a finding.
	ItemPrefix string
	// ObjectWrittenNameEqualsValue says an object is written as
	// name=value items, R=100,G=200, rather than R,100,G,200.
	ObjectWrittenNameEqualsValue bool
	// ContentReads, when not 0, says the parameter is described with
	// content, and its value is read this way, as JSON, a form or text,
	// before its check. The style fields are then all empty.
	ContentReads Reads
	// Spread says an object cookie is one cookie per property, each named
	// by the property. Only the schema's own properties are read.
	Spread bool
	// Decode says each cookie value is percent-decoded before it is
	// checked; without it a cookie is checked as written.
	Decode bool
	// Schema is nil when the spec gives none: the parameter is then only
	// checked for being there.
	Schema Schema
	// Label names the parameter in a problem, such as "query limit".
	Label string
}

// Content is one type a body may be sent as.
type Content struct {
	// Schema is nil when the spec gives none: the body is then only
	// checked to be readable.
	Schema Schema
	// XML is how the body is read as XML, nil when it can't be XML.
	XML *read.XMLBody
	// Marked are the fields the spec marks x-hotline-record.
	Marked []Marked
	// Label names the content in a problem, such as "the request body
	// application/json".
	Label string
}

// Marked is one body field the spec marks x-hotline-record. Its value is
// copied when it is of one of the types; read.Plain turns it into the
// Field's value.
type Marked struct {
	// Path is the property names from the top: ["box", "weight"]. A
	// field inside a list is never marked.
	Path []string
	// Name is the record attribute it is copied to: shop.weight.
	Name string
	// Types are string, number, integer or boolean; null is left out.
	Types []string
}
