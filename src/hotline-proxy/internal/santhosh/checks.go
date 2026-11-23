package santhosh

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/spec/read"
)

// whole names a whole body in a finding.
const whole = "$"

// partBody is the part of the findings about a body.
const partBody = "body"

// param is a parameter or response header with its compiled schema, nil
// when the spec gives none.
type param struct {
	spec.Param
	schema *jsonschema.Schema
	notes  branchNotes
	// list says the schema wants a list, and object an object.
	list   bool
	object bool
}

// content is one type a body may be sent as, with its compiled schema.
type content struct {
	spec.Content
	schema *jsonschema.Schema
	notes  branchNotes
}

// requestCheck checks the request side of one route, a spec.RequestCheck.
type requestCheck struct {
	params []param
	body   []content
}

// responseCheck checks one response, a spec.ResponseCheck.
type responseCheck struct {
	headers []param
	body    []content
}

// Request builds the check of one route's request side.
func (e *Engine) Request(r spec.RequestSpec) (spec.RequestCheck, error) {
	params, paramsErr := e.params(r.Params)
	body, bodyErr := e.contents(r.Body)
	return &requestCheck{params: params, body: body}, errors.Join(paramsErr, bodyErr)
}

// Response builds the check of one response the spec lists.
func (e *Engine) Response(r spec.ResponseSpec) (spec.ResponseCheck, error) {
	headers, headersErr := e.params(r.Headers)
	body, bodyErr := e.contents(r.Body)
	return &responseCheck{headers: headers, body: body}, errors.Join(headersErr, bodyErr)
}

// errNoProperties marks an object cookie, read one cookie per property,
// whose schema names no properties.
var errNoProperties = errors.New("the schema names no properties, so the cookies of the object cannot be found")

func (e *Engine) params(list []spec.Param) ([]param, error) {
	out := make([]param, len(list))
	var problems []error
	for i, p := range list {
		s, err := e.schema(p.Schema, p.Label)
		problems = append(problems, err)
		out[i] = param{Param: p, schema: s, notes: e.notes, list: slices.Contains(types(s), "array"), object: slices.Contains(types(s), "object")}
		if p.Spread && out[i].object && properties(s) == nil {
			// One cookie per property needs the property names.
			problems = append(problems, fmt.Errorf("%w: %s", errNoProperties, p.Label))
		}
	}
	return out, errors.Join(problems...)
}

func (e *Engine) contents(list []spec.Content) ([]content, error) {
	out := make([]content, len(list))
	var problems []error
	for i, c := range list {
		s, err := e.schema(c.Schema, c.Label)
		problems = append(problems, err)
		out[i] = content{Content: c, schema: s, notes: e.notes}
	}
	return out, errors.Join(problems...)
}

func (q *requestCheck) Params(escapedPath, rawQuery string, header http.Header) []spec.Finding {
	var query url.Values
	if rawQuery != "" {
		query, _ = url.ParseQuery(rawQuery)
	}
	cookies := read.Cookies(header)
	var findings []spec.Finding
	for _, p := range q.params {
		if p.WholeQuery != 0 {
			findings = append(findings, p.checkWhole(rawQuery)...)
			continue
		}
		findings = append(findings, p.check(p.values(escapedPath, query, header, cookies))...)
	}
	return findings
}

func (q *requestCheck) Body(i int, reads spec.Reads, body []byte) ([]spec.Finding, []spec.Field) {
	return q.body[i].check(reads, body)
}

func (r *responseCheck) Headers(header http.Header) []spec.Finding {
	var findings []spec.Finding
	for _, h := range r.headers {
		findings = append(findings, h.check(h.values("", nil, header, nil))...)
	}
	return findings
}

func (r *responseCheck) Body(i int, reads spec.Reads, body []byte) ([]spec.Finding, []spec.Field) {
	return r.body[i].check(reads, body)
}

// values are the parameter's values in the call, nil when it is missing:
// text, or an object for a query written filter[status]=x or a cookie
// object.
func (p param) values(escapedPath string, query url.Values, header http.Header, cookies map[string][]string) any {
	switch p.In {
	case "path":
		if value, ok := read.PathValue(escapedPath, p.Segment, p.Rest); ok {
			return []string{value}
		}
	case "query":
		if p.DeepObject {
			return read.DeepObject(query, p.Name)
		}
		if values, ok := query[p.Name]; ok {
			return values
		}
	case "header":
		if value, ok := read.HeaderValue(header, p.Name); ok {
			return []string{value}
		}
	case "cookie":
		return p.cookie(cookies)
	}
	return nil
}

// cookie is the cookie parameter's value: its text values, or an object
// when the schema wants one; nil when it is missing.
func (p param) cookie(cookies map[string][]string) any {
	if p.object && p.Spread {
		// One cookie per property. Only the schema's own properties are
		// taken, so other cookies of the call are never extra properties.
		object := map[string]any{}
		for name := range properties(p.schema) {
			if values, ok := cookies[name]; ok {
				object[name] = p.decoded(values[0])
			}
		}
		if len(object) == 0 {
			return nil
		}
		return object
	}
	values, ok := cookies[p.Name]
	if !ok {
		return nil
	}
	if p.object {
		return read.Pairs(p.decoded(values[0]))
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = p.decoded(v)
	}
	return out
}

// properties are the properties a schema names, following $ref.
func properties(s *jsonschema.Schema) map[string]*jsonschema.Schema {
	for ; s != nil; s = s.Ref {
		if s.Properties != nil {
			return s.Properties
		}
	}
	return nil
}

// decoded is a cookie value percent-decoded when its style says so. A
// value that does not decode is checked as written.
func (p param) decoded(value string) string {
	if !p.Decode {
		return value
	}
	if plain, err := url.PathUnescape(value); err == nil {
		return plain
	}
	return value
}

func (p param) check(values any) []spec.Finding {
	if values == nil {
		if p.Required {
			return []spec.Finding{{Part: p.In, Place: p.Name, Rule: "required"}}
		}
		return nil
	}
	if p.schema == nil {
		return nil
	}
	instance := values
	if list, ok := values.([]string); ok {
		instance = read.Shape(list, p.list, p.Split)
	}
	return validate(p.schema, p.notes, coerce(instance, p.schema), p.In, p.Name)
}

// checkWhole checks the whole query string, as its media type reads it.
// Its places read like those of query parameters: limit, not $.limit;
// the whole value is $.
func (p param) checkWhole(rawQuery string) []spec.Finding {
	if rawQuery == "" {
		if p.Required {
			return []spec.Finding{{Part: p.In, Place: whole, Rule: "required"}}
		}
		return nil
	}
	instance, err := readWhole(rawQuery, p.WholeQuery)
	if err != nil {
		return []spec.Finding{{Part: p.In, Place: whole, Rule: "querystring"}}
	}
	if p.schema == nil {
		return nil
	}
	findings := validate(p.schema, p.notes, coerce(instance, p.schema), p.In, whole)
	for i := range findings {
		if name, ok := strings.CutPrefix(findings[i].Place, whole+"."); ok {
			findings[i].Place = name
		}
	}
	return findings
}

// readWhole reads a query string: a form, or JSON or text percent-decoded
// as a form is, with + a space, as browsers and Go's url package write
// it.
func readWhole(rawQuery string, reads spec.Reads) (any, error) {
	if reads == spec.ReadsForm {
		form, err := read.Form([]byte(rawQuery))
		return form, err
	}
	text, err := url.QueryUnescape(rawQuery)
	switch {
	case err != nil:
		return nil, err
	case reads == spec.ReadsJSON:
		return decodeJSON([]byte(text))
	}
	return text, nil
}

// check reads a body and checks it against the schema of its content,
// copying its marked fields.
func (c content) check(reads spec.Reads, data []byte) ([]spec.Finding, []spec.Field) {
	var instance any
	switch reads {
	case spec.ReadsXML:
		var err error
		instance, err = c.XML.Read(data)
		var root *read.RootError
		switch {
		case errors.As(err, &root):
			return []spec.Finding{{Part: partBody, Place: whole, Rule: "xml_root", Expected: root.Want}}, nil
		case err != nil:
			return []spec.Finding{{Part: partBody, Place: whole, Rule: "xml"}}, nil
		case c.XML.Shape == nil:
			return nil, nil
		}
		instance = coerce(instance, c.schema)
	case spec.ReadsForm:
		values, err := read.Form(data)
		if err != nil {
			return []spec.Finding{{Part: partBody, Place: whole, Rule: "form"}}, nil
		}
		instance = coerce(values, c.schema)
	default: // spec.ReadsJSON
		if c.schema == nil {
			// No schema, so no marked fields: the body need only be JSON.
			if !json.Valid(data) {
				return []spec.Finding{{Part: partBody, Place: whole, Rule: "json"}}, nil
			}
			return nil, nil
		}
		var err error
		if instance, err = decodeJSON(data); err != nil {
			return []spec.Finding{{Part: partBody, Place: whole, Rule: "json"}}, nil
		}
	}
	// The marked fields are picked from the value santhosh needs whole
	// anyway, so the body is read once.
	var fields []spec.Field
	for _, m := range c.Marked {
		if value, ok := read.Plain(read.At(instance, m.Path), m.Types); ok {
			fields = append(fields, spec.Field{Name: m.Name, Value: value})
		}
	}
	if c.schema == nil {
		return nil, fields
	}
	return validate(c.schema, c.notes, instance, partBody, whole), fields
}
