// Package read turns the parts of a call into the Go data a JSON Schema
// checks: an XML or form body, and the values of path, query, header and
// cookie parameters, as OpenAPI writes them. It also turns a value copied
// from a body into a record's value. A schema engine may use it, or read
// the call its own way.
package read
