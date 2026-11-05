package spec

// The RequestCheck and ResponseCheck made with the santhosh package, which
// alone knows the santhosh-tekuri JSON Schema library.

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"hotline-proxy/internal/santhosh"
)

// schemas compiles the schemas of one spec, and builds the checks of each
// route from them.
type schemas struct {
	b        *builder
	compiler *santhosh.Compiler
}

// newSchemas gives the spec, read as plain data, to the compiler. The
// schemas built when that fails are nil, and check nothing; the problem is
// reported by the caller.
func newSchemas(b *builder) (*schemas, error) {
	compiler, err := santhosh.NewCompiler(b.doc)
	return &schemas{b: b, compiler: compiler}, err
}

// request builds the check of a route's request side.
func (s *schemas) request(spec *Spec, r route, where string) RequestCheck {
	return &santhoshRequest{
		spec:   spec,
		route:  r,
		params: s.params(r.checks.params, where),
		body:   s.body(r.checks.body, where),
	}
}

// response builds the check of one reply the spec lists.
func (s *schemas) response(rp *reply, where string) ResponseCheck {
	return &santhoshResponse{headers: s.params(rp.headers, where), body: s.body(rp.body, where)}
}

func (s *schemas) params(params []param, where string) []santhoshParam {
	out := make([]santhoshParam, len(params))
	for i, p := range params {
		out[i] = santhoshParam{param: p, compiled: s.schema(p.schema, where)}
	}
	return out
}

func (s *schemas) body(b *bodySpec, where string) santhoshBody {
	if b == nil {
		return santhoshBody{}
	}
	compiled := make([]*santhosh.Schema, len(b.content))
	for i, c := range b.content {
		compiled[i] = s.schema(c.schema, where)
	}
	return santhoshBody{spec: b, schemas: compiled}
}

// schema compiles a schema of the spec, nil when there is none.
func (s *schemas) schema(at schemaAt, where string) *santhosh.Schema {
	if !at.found || s.compiler == nil {
		return nil
	}
	compiled, err := s.compiler.Compile(at.value, at.pointer)
	if err != nil {
		s.b.problem("%s: the schema of %s cannot be used: %v", where, at.what, err)
	}
	return compiled
}

// components compiles every shared schema, so a broken one is found even
// when no route uses it.
func (s *schemas) components() {
	all, _ := s.b.at("/components/schemas")
	m, _ := all.(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if err := s.compiler.CompileAt("/components/schemas" + token(name)); err != nil {
			s.b.problem("%s: the schema %s cannot be used: %v", s.b.name, name, err)
		}
	}
}

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
	compiled *santhosh.Schema
}

// santhoshBody holds the compiled schema of each content a body may have,
// in the order of spec.content.
type santhoshBody struct {
	spec    *bodySpec
	schemas []*santhosh.Schema
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
	instance = p.compiled.Coerce(instance)
	return resultsOf(part, p.compiled.Validate(instance, p.name))
}

// shape turns the strings of a parameter into the value its schema wants:
// a list for an array, the first value otherwise.
func (p santhoshParam) shape(list []string) any {
	if !slices.Contains(p.compiled.Types(), "array") {
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
		instance = schema.Coerce(instance)
	case jsonBodyKind:
		var err error
		instance, err = decodeJSON(data)
		if err != nil {
			return []Result{{Part: partBody, Place: whole, Rule: "json"}}, nil
		}
	case formBodyKind:
		values, err := url.ParseQuery(string(data))
		if err != nil {
			return []Result{{Part: partBody, Place: whole, Rule: "form"}}, nil
		}
		instance = schema.Coerce(nest(values))
	default:
		return nil, nil
	}
	copied := c.copied(instance)
	if schema == nil {
		return nil, copied
	}
	return resultsOf(partBody, schema.Validate(instance, whole)), copied
}

// resultsOf turns the failures of one part of a call into results.
func resultsOf(part string, failures []santhosh.Failure) []Result {
	if len(failures) == 0 {
		return nil
	}
	results := make([]Result, len(failures))
	for i, f := range failures {
		results[i] = Result{Part: part, Place: f.Place, Rule: f.Rule, Expected: f.Expected}
	}
	return results
}
