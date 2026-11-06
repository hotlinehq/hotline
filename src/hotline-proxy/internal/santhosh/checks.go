package santhosh

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/spec/read"
)

// whole names a whole body in a result.
const whole = "$"

// partBody is the part of the results about a body.
const partBody = "body"

// param is a parameter or reply header with its compiled schema, nil
// when the spec gives none.
type param struct {
	spec.Param
	schema *jsonschema.Schema
	// list says the schema wants a list.
	list bool
}

// content is one type a body may be sent as, with its compiled schema.
type content struct {
	spec.Content
	schema *jsonschema.Schema
}

// requestCheck checks the request side of one route, a spec.RequestCheck.
type requestCheck struct {
	params []param
	body   []content
}

// responseCheck checks one reply, a spec.ResponseCheck.
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

// Response builds the check of one reply the spec lists.
func (e *Engine) Response(r spec.ResponseSpec) (spec.ResponseCheck, error) {
	headers, headersErr := e.params(r.Headers)
	body, bodyErr := e.contents(r.Body)
	return &responseCheck{headers: headers, body: body}, errors.Join(headersErr, bodyErr)
}

func (e *Engine) params(list []spec.Param) ([]param, error) {
	out := make([]param, len(list))
	var problems []error
	for i, p := range list {
		s, err := e.schema(p.Schema, p.What)
		problems = append(problems, err)
		out[i] = param{Param: p, schema: s, list: slices.Contains(types(s), "array")}
	}
	return out, errors.Join(problems...)
}

func (e *Engine) contents(list []spec.Content) ([]content, error) {
	out := make([]content, len(list))
	var problems []error
	for i, c := range list {
		s, err := e.schema(c.Schema, c.What)
		problems = append(problems, err)
		out[i] = content{Content: c, schema: s}
	}
	return out, errors.Join(problems...)
}

func (q *requestCheck) Meta(escapedPath, rawQuery string, header http.Header) []spec.Result {
	var query url.Values
	if rawQuery != "" {
		query, _ = url.ParseQuery(rawQuery)
	}
	cookies := map[string]string{}
	read.Cookies(header, cookies)
	var results []spec.Result
	for _, p := range q.params {
		results = append(results, p.check(p.values(escapedPath, query, header, cookies))...)
	}
	return results
}

func (q *requestCheck) Body(i int, reads spec.Reads, body []byte) ([]spec.Result, []spec.Field) {
	return q.body[i].check(reads, body)
}

func (r *responseCheck) Meta(header http.Header) []spec.Result {
	var results []spec.Result
	for _, h := range r.headers {
		results = append(results, h.check(h.values("", nil, header, nil))...)
	}
	return results
}

func (r *responseCheck) Body(i int, reads spec.Reads, body []byte) ([]spec.Result, []spec.Field) {
	return r.body[i].check(reads, body)
}

// values are the parameter's values in the call, nil when it is missing:
// text, or an object for a query written filter[status]=x.
func (p param) values(escapedPath string, query url.Values, header http.Header, cookies map[string]string) any {
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
		if value, ok := cookies[p.Name]; ok {
			return []string{value}
		}
	}
	return nil
}

func (p param) check(values any) []spec.Result {
	if values == nil {
		if p.Required {
			return []spec.Result{{Part: p.In, Place: p.Name, Rule: "required"}}
		}
		return nil
	}
	if p.schema == nil {
		return nil
	}
	instance := values
	if list, ok := values.([]string); ok {
		instance = read.Shape(list, p.list, !p.Explode || p.In != "query")
	}
	return validate(p.schema, coerce(instance, p.schema), p.In, p.Name)
}

// check reads a body and checks it against the schema of its content,
// copying its marked fields.
func (c content) check(reads spec.Reads, data []byte) ([]spec.Result, []spec.Field) {
	var instance any
	switch reads {
	case spec.ReadsXML:
		var err error
		instance, err = c.XML.Read(data)
		var root *read.RootError
		switch {
		case errors.As(err, &root):
			return []spec.Result{{Part: partBody, Place: whole, Rule: "xml_root", Expected: root.Want}}, nil
		case err != nil:
			return []spec.Result{{Part: partBody, Place: whole, Rule: "xml"}}, nil
		case c.XML.Shape == nil:
			return nil, nil
		}
		instance = coerce(instance, c.schema)
	case spec.ReadsForm:
		values, err := read.Form(data)
		if err != nil {
			return []spec.Result{{Part: partBody, Place: whole, Rule: "form"}}, nil
		}
		instance = coerce(values, c.schema)
	default: // spec.ReadsJSON
		if c.schema == nil {
			// No schema, so no marked fields: the body need only be JSON.
			if !json.Valid(data) {
				return []spec.Result{{Part: partBody, Place: whole, Rule: "json"}}, nil
			}
			return nil, nil
		}
		var err error
		if instance, err = decodeJSON(data); err != nil {
			return []spec.Result{{Part: partBody, Place: whole, Rule: "json"}}, nil
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
	return validate(c.schema, instance, partBody, whole), fields
}
