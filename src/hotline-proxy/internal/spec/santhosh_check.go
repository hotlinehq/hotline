package spec

// The checks made with the santhosh-tekuri JSON Schema library, compiled in
// santhosh_compile.go.

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// santhoshRequest checks the request side of one route.
type santhoshRequest struct {
	spec   *Spec
	route  route
	params []santhoshParam
	body   santhoshBody
}

// santhoshResponse checks one reply the spec lists.
type santhoshResponse struct {
	headers []santhoshParam
	body    santhoshBody
}

// santhoshParam is a parameter or reply header with its compiled schema,
// nil when the spec gives none.
type santhoshParam struct {
	param
	compiled *jsonschema.Schema
}

// santhoshBody holds the compiled schema of each content a body may have,
// in the order of spec.content.
type santhoshBody struct {
	spec    *bodySpec
	schemas []*jsonschema.Schema
}

func (q *santhoshRequest) Meta(escapedPath, rawQuery string, header http.Header) []Result {
	query, _ := url.ParseQuery(rawQuery)
	cookies := map[string]string{}
	readCookies(header, cookies)
	segments := q.route.split(strings.TrimSuffix(escapedPath, "/"))
	var results []Result
	for _, p := range q.params {
		results = append(results, p.check(p.values(q.route, segments, query, header, cookies))...)
	}
	results = append(results, q.spec.unknownQuery(q.route, query)...)
	return append(results, q.route.checks.loggedIn(header, query, cookies)...)
}

func (q *santhoshRequest) Body(mediaType string, body []byte) ([]Result, []Field) {
	return q.body.check(mediaType, body)
}

func (r *santhoshResponse) Meta(header http.Header) []Result {
	var results []Result
	for _, h := range r.headers {
		results = append(results, h.check(h.values(route{}, nil, nil, header, nil))...)
	}
	return results
}

func (r *santhoshResponse) Body(mediaType string, body []byte) ([]Result, []Field) {
	return r.body.check(mediaType, body)
}

func (p santhoshParam) check(values any) []Result {
	part := p.in
	if values == nil {
		if p.required {
			return []Result{{Part: part, Place: p.name, Rule: "required"}}
		}
		return nil
	}
	if p.compiled == nil {
		return nil
	}
	instance := values
	if list, ok := values.([]string); ok {
		instance = p.shape(list)
	}
	instance = coerce(instance, p.compiled)
	return validate(p.compiled, instance, part, p.name)
}

// shape turns the strings of a parameter into the value its schema wants:
// a list for an array, the first value otherwise.
func (p santhoshParam) shape(list []string) any {
	if !slices.Contains(types(p.compiled), "array") {
		return list[0]
	}
	if !p.explode || p.in != "query" {
		list = strings.Split(strings.Join(list, ","), ",")
	}
	items := make([]any, len(list))
	for i, v := range list {
		items[i] = v
	}
	return items
}

// check reads a body of the given media type, copies its marked fields
// and checks it against the schema of its content. The body is one the
// spec lists a content for, and small enough to check.
func (b santhoshBody) check(mediaType string, data []byte) ([]Result, []Field) {
	if b.spec == nil {
		return nil, nil
	}
	c, i := b.spec.find(mediaType)
	if c == nil {
		return nil, nil
	}
	schema := b.schemas[i]
	var instance any
	switch c.kind(mediaType) {
	case xmlBodyKind:
		var problem *Result
		instance, problem = c.xml.read(data)
		if problem != nil {
			return []Result{*problem}, nil
		}
		if c.xml.shape == nil {
			return nil, nil
		}
		instance = coerce(instance, schema)
	case jsonBodyKind:
		var err error
		instance, err = jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return []Result{{Part: partBody, Place: whole, Rule: "json"}}, nil
		}
	case formBodyKind:
		values, err := url.ParseQuery(string(data))
		if err != nil {
			return []Result{{Part: partBody, Place: whole, Rule: "form"}}, nil
		}
		instance = coerce(nest(values), schema)
	default:
		return nil, nil
	}
	copied := c.copied(instance)
	if schema == nil {
		return nil, copied
	}
	return validate(schema, instance, partBody, whole), copied
}

// coerce turns text from a query, header, cookie or form into the number
// or true/false the schema wants, so the schema can check it.
func coerce(v any, s *jsonschema.Schema) any {
	if s == nil {
		return v
	}
	for s.Ref != nil && s.Types == nil && s.Properties == nil && s.Items2020 == nil {
		s = s.Ref
	}
	switch x := v.(type) {
	case string:
		return scalar(x, types(s))
	case []any:
		for i := range x {
			item := s.Items2020
			if i < len(s.PrefixItems) {
				item = s.PrefixItems[i]
			}
			x[i] = coerce(x[i], item)
		}
	case map[string]any:
		for key := range x {
			prop := s.Properties[key]
			if extra, ok := s.AdditionalProperties.(*jsonschema.Schema); ok && prop == nil {
				prop = extra
			}
			x[key] = coerce(x[key], prop)
		}
	}
	return v
}

// types are the types a schema allows, following $ref.
func types(s *jsonschema.Schema) []string {
	for ; s != nil; s = s.Ref {
		if s.Types != nil {
			return s.Types.ToStrings()
		}
	}
	return nil
}

// validate checks a value against a schema and turns what is wrong into
// results. base names the value: a parameter's name, or $ for a body.
func validate(s *jsonschema.Schema, instance any, part, base string) []Result {
	var invalid *jsonschema.ValidationError
	if !errors.As(s.Validate(instance), &invalid) {
		return nil
	}
	var leaves []*jsonschema.ValidationError
	collect(invalid, &leaves)
	var results []Result
	for _, e := range leaves {
		place := placeOf(base, instance, e.InstanceLocation)
		switch k := e.ErrorKind.(type) {
		case *kind.Required:
			for _, name := range k.Missing {
				results = append(results, Result{Part: part, Place: place + "." + name, Rule: "required"})
			}
		case *kind.AdditionalProperties:
			for _, name := range k.Properties {
				results = append(results, Result{Part: part, Place: place + "." + name, Rule: "additionalProperties"})
			}
		case *kind.DependentRequired:
			for _, name := range k.Missing {
				results = append(results, Result{Part: part, Place: place + "." + name, Rule: "dependentRequired"})
			}
		case *kind.ContentMediaType:
			// A string that should hold JSON doesn't.
			results = append(results, Result{Part: part, Place: place, Rule: "json"})
		case *kind.ContentEncoding:
			// A string that should be base64 text isn't.
			results = append(results, Result{Part: part, Place: place, Rule: "base64"})
		default:
			results = append(results, Result{Part: part, Place: place, Rule: rule(e.ErrorKind), Expected: expected(e.ErrorKind)})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Place < results[j].Place })
	return results
}

// rule names the broken rule by its JSON Schema keyword.
func rule(k jsonschema.ErrorKind) string {
	switch k.(type) {
	case *kind.Not:
		return "not"
	case *kind.FalseSchema:
		return "false"
	}
	path := append([]string{"schema"}, k.KeywordPath()...)
	return path[len(path)-1]
}

// collect finds the errors that say what is wrong, leaving out those that
// only group others. For anyOf, oneOf and not the error itself is kept:
// the errors of each choice would only confuse.
func collect(e *jsonschema.ValidationError, leaves *[]*jsonschema.ValidationError) {
	switch e.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf, *kind.Not:
		*leaves = append(*leaves, e)
		return
	}
	if len(e.Causes) == 0 {
		*leaves = append(*leaves, e)
		return
	}
	for _, cause := range e.Causes {
		collect(cause, leaves)
	}
}

// placeOf writes where a value sits: $.lines[1].sku in a body, tag[1] or
// filter.status in a parameter.
func placeOf(base string, instance any, location []string) string {
	var b strings.Builder
	b.WriteString(base)
	for _, token := range location {
		if token == insideJSON {
			// The value is JSON text; the place carries on inside it.
			if text, ok := instance.(string); ok {
				instance, _ = jsonschema.UnmarshalJSON(strings.NewReader(text))
			}
			continue
		}
		if list, ok := instance.([]any); ok {
			i, _ := strconv.Atoi(token)
			b.WriteString("[" + token + "]")
			instance = list[i]
			continue
		}
		b.WriteString("." + token)
		if m, ok := instance.(map[string]any); ok {
			instance = m[token]
		}
	}
	return b.String()
}

// expected is what the spec asks for, taken from the schema, never from
// the value.
func expected(k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.Type:
		return strings.Join(k.Want, " or ")
	case *kind.Format:
		return k.Want
	case *kind.Enum:
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = fmt.Sprint(w)
		}
		return strings.Join(want, ", ")
	case *kind.Const:
		return fmt.Sprint(k.Want)
	case *kind.Pattern:
		return k.Want
	case *kind.Minimum:
		return k.Want.RatString()
	case *kind.Maximum:
		return k.Want.RatString()
	case *kind.ExclusiveMinimum:
		return k.Want.RatString()
	case *kind.ExclusiveMaximum:
		return k.Want.RatString()
	case *kind.MultipleOf:
		return k.Want.RatString()
	case *kind.MinLength:
		return strconv.Itoa(k.Want)
	case *kind.MaxLength:
		return strconv.Itoa(k.Want)
	case *kind.MinItems:
		return strconv.Itoa(k.Want)
	case *kind.MaxItems:
		return strconv.Itoa(k.Want)
	case *kind.MinProperties:
		return strconv.Itoa(k.Want)
	case *kind.MaxProperties:
		return strconv.Itoa(k.Want)
	}
	return ""
}
