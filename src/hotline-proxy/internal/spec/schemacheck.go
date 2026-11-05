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
