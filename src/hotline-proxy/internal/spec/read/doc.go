// Package read turns the parts of a call into the Go data a JSON Schema
// checks: an XML or form body, and the values of path, query, header and
// cookie parameters, as OpenAPI writes them. It also turns a value copied
// from a body into a record's value. A schema engine may use it, or read
// the call its own way.
package read

// maxDepth is how deep a body is read: the names in one form field, or
// the open elements in XML. Reading goes one level deeper in memory for
// each, so without a limit a 2 MB body nested very deep took some 300 MB
// (security scan, 2026-11-18). Real bodies stay far below it.
const maxDepth = 64
