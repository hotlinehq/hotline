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
	// Response builds the check of one reply the spec lists.
	Response(r ResponseSpec) (ResponseCheck, error)
}

// RequestCheck checks the request side of one route. It is safe to use
// from many goroutines at once.
type RequestCheck interface {
	// Meta checks the parameters: each one there when required, and its
	// value against its schema. It gets the request as Go's http server
	// gives it, unparsed.
	Meta(escapedPath, rawQuery string, header http.Header) []Result
	// Body checks a body and copies its marked fields. content is the
	// place in the route's Body list of the content it was sent as, and
	// reads how to read it; spec has found both, and the body is not empty
	// and small enough to check.
	Body(content int, reads Reads, body []byte) ([]Result, []Field)
}

// ResponseCheck checks one reply the spec lists, the way RequestCheck
// checks a request.
type ResponseCheck interface {
	// Meta checks the reply's headers.
	Meta(header http.Header) []Result
	// Body checks a reply body, as RequestCheck.Body does.
	Body(content int, reads Reads, body []byte) ([]Result, []Field)
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
)

// RequestSpec is what the spec says about one route's request.
type RequestSpec struct {
	Params []Param
	// Body holds each content the request body may be sent as, in the
	// order RequestCheck.Body numbers them. Empty when the spec lists no
	// body.
	Body []Content
}

// ResponseSpec is what the spec says about one reply.
type ResponseSpec struct {
	// Headers are the reply's headers, each with In "header".
	Headers []Param
	Body    []Content
}

// Param is one parameter of a route, or a header of a reply.
type Param struct {
	Name string
	// In is path, query, header or cookie. It is also the Part of the
	// results about it.
	In       string
	Required bool
	// Segment is, for a path value, which piece of the path split at "/"
	// holds it, -1 when the path has none. Rest says the value takes the
	// rest of the path from there.
	Segment int
	Rest    bool
	// Explode says a query list is written tag=a&tag=b; without it,
	// tag=a,b. A header list is always written with commas.
	Explode bool
	// DeepObject says a query object is written filter[status]=x.
	DeepObject bool
	// Schema is nil when the spec gives none: the parameter is then only
	// checked for being there.
	Schema Schema
	// What names the parameter in a problem, such as "query limit".
	What string
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
	// What names the content in a problem, such as "the request body
	// application/json".
	What string
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
