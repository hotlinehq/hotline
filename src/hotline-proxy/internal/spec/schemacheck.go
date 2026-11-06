package spec

import "net/http"

// RequestCheck checks the request side of one route against the schemas of
// the spec. It gets the request as Go's http server gives it, unparsed, so
// each way of checking reads it as it likes. The checks that need no
// schema, such as finding the route and whether the spec lists a body, are
// made before it is called.
type RequestCheck interface {
	// Meta checks the path, query and headers: each parameter, the query
	// names the spec does not know, and the credentials of a way to log
	// in. Cookies are read from the Cookie header.
	Meta(escapedPath, rawQuery string, header http.Header) []Result
	// Body checks a body against the schema of its content, and copies
	// the fields the spec marks x-hotline-record. mediaType is the body's
	// type as Content-Type names it, without parameters. It is called
	// only for a body the spec lists a content for, small enough to check.
	Body(mediaType string, body []byte) ([]Result, []Field)
}

// ResponseCheck checks one reply the spec lists against its schemas, the
// way RequestCheck checks a request.
type ResponseCheck interface {
	// Meta checks the reply's headers.
	Meta(header http.Header) []Result
	// Body checks a reply body, as RequestCheck.Body does.
	Body(mediaType string, body []byte) ([]Result, []Field)
}

// SchemaCompiler compiles the JSON Schemas of one spec. It is made by a
// NewSchemaCompiler and used while the spec is loaded, by one goroutine.
type SchemaCompiler interface {
	// Compile compiles the schema v, found in the spec at pointer.
	Compile(v any, pointer string) (Schema, error)
	// CompileAt compiles the schema at pointer, such as a shared one under
	// /components/schemas, so a broken one is found even when nothing
	// uses it.
	CompileAt(pointer string) error
}

// NewSchemaCompiler makes the compiler of one spec, given the spec as
// plain JSON data with its numbers as json.Number.
type NewSchemaCompiler func(doc any) (SchemaCompiler, error)

// Schema is one compiled schema. It is safe to use from many goroutines at
// once.
type Schema interface {
	// Coerce turns text from a query, header, cookie, form or XML body
	// into the number or true/false the schema wants at each place, so the
	// schema can check it. Lists and objects are changed in place.
	Coerce(v any) any
	// Types are the types the schema allows, following $ref.
	Types() []string
	// Validate checks a value and says what is wrong with it, sorted by
	// place. base names the value: a parameter's name, or $ for a body.
	Validate(v any, base string) []Failure
	// CheckJSON reads a JSON body and checks it, as Validate does with base
	// $, in one pass. As it reads, it picks out the value at each of the
	// places, a path of property names, so the fields the spec marks
	// x-hotline-record can be copied without reading the body again. Each
	// value is a string, a bool, a json.Number as written, or nil when
	// there is none or it is something else. ok is false when the body is
	// not JSON. Like Go's json package, it lets a name come twice, the last
	// one winning.
	CheckJSON(data []byte, places [][]string) (failures []Failure, values []any, ok bool)
}

// Failure is one thing a value breaks: the place in it, the rule it broke
// and what the schema expects. It never holds the value itself.
type Failure struct {
	// Place starts with the base given to Validate: $.lines[1].sku in a
	// body, tag[1] or filter.status in a parameter.
	Place    string
	Rule     string
	Expected string
}

// schemaAt is a schema of the spec as it is written, and where it is, for
// a RequestCheck or ResponseCheck to build its check from.
type schemaAt struct {
	value any
	// found is false when the spec gives no schema.
	found   bool
	pointer string
	// what names it in a problem, such as "query limit".
	what string
}

// schemaOf is the schema an owner, such as a parameter, holds.
func schemaOf(owner map[string]any, pointer, what string) schemaAt {
	v, found := owner["schema"]
	return schemaAt{value: v, found: found, pointer: pointer + "/schema", what: what}
}
