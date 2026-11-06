package spec

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// builder turns the spec, read as plain data, into the checks of each
// route. Its schemas are compiled once, and $ref is followed only within
// the spec.
type builder struct {
	name string
	doc  any
	// schemas builds the schema checks of each route.
	schemas  *schemas
	schemes  map[string]*credential
	problems []error
	// reported holds the x-hotline-record marks already found broken.
	reported map[string]bool
}

func newBuilder(name string, data []byte, compiler NewSchemaCompiler) *builder {
	b := &builder{name: name, schemes: map[string]*credential{}, reported: map[string]bool{}}
	var raw any
	_ = yaml.Unmarshal(data, &raw)
	plain, err := json.Marshal(normalize(raw))
	if err == nil {
		b.doc, err = decodeJSON(plain)
	}
	var started error
	b.schemas, started = newSchemas(b, compiler)
	if err = cmp.Or(err, started); err != nil {
		b.schemas.compiler = nil
		b.problem("%s cannot be read as JSON Schema: %v", name, err)
		return b
	}
	b.readSchemes()
	b.schemas.components()
	return b
}

// decodeJSON reads JSON data with its numbers kept as json.Number, as
// written. It reads the data where it is: a json.Decoder would first copy it
// whole into a buffer of its own, more memory than the value it gives.
// Like json.Decoder, it lets a name come twice, the last one winning, and
// takes text that is not UTF-8.
func decodeJSON(data []byte) (any, error) {
	d := jsontext.NewDecoder(bytes.NewBuffer(data), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	v, err := readValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errTrailing
	}
	return v, nil
}

// readValue reads the next JSON value from d as plain Go data.
func readValue(d *jsontext.Decoder) (any, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return nil, nil //nolint:nilnil // JSON null is a nil value, not a missing one
	case jsontext.KindFalse, jsontext.KindTrue:
		return tok.Bool(), nil
	case jsontext.KindString:
		return tok.String(), nil
	case jsontext.KindNumber:
		return json.Number(tok.String()), nil
	case jsontext.KindBeginObject:
		object := map[string]any{}
		for d.PeekKind() != jsontext.KindEndObject {
			name, err := d.ReadToken()
			if err != nil {
				return nil, err
			}
			key := name.String() // before the next read, which reuses its memory
			if object[key], err = readValue(d); err != nil {
				return nil, err
			}
		}
		_, err = d.ReadToken()
		return object, err
	default: // jsontext.KindBeginArray, as the decoder gives no other kind first
		list := []any{}
		for d.PeekKind() != jsontext.KindEndArray {
			item, err := readValue(d)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
		_, err = d.ReadToken()
		return list, err
	}
}

// errTrailing marks JSON data with more after its value.
var errTrailing = errors.New("more data after the JSON value")

// normalize makes YAML data plain JSON data: every object key a string.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for key, child := range x {
			x[key] = normalize(child)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for key, child := range x {
			m[fmt.Sprint(key)] = normalize(child)
		}
		return m
	case []any:
		for i, child := range x {
			x[i] = normalize(child)
		}
	}
	return v
}

func (b *builder) problem(format string, args ...any) {
	b.problems = append(b.problems, fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...)))
}

// token is one step of a JSON pointer, with / and ~ escaped.
func token(name string) string {
	return "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

// at finds the value at a JSON pointer, such as /components/schemas/Order.
func (b *builder) at(pointer string) (any, bool) {
	v := b.doc
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		var found bool
		switch x := v.(type) {
		case map[string]any:
			v, found = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			found = err == nil && i >= 0 && i < len(x)
			if found {
				v = x[i]
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}

// object follows any $ref and returns the object found, with its pointer.
// A $ref to another file, or to nothing, is a problem.
func (b *builder) object(v any, pointer, where string) (map[string]any, string) {
	for range 16 {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, ""
		}
		ref, isRef := m["$ref"].(string)
		if !isRef {
			return m, pointer
		}
		if !strings.HasPrefix(ref, "#") {
			b.problem("%s: the $ref %s points to another file", where, ref)
			return nil, ""
		}
		pointer, _ = url.PathUnescape(strings.TrimPrefix(ref, "#"))
		if v, ok = b.at(pointer); !ok {
			b.problem("%s: the $ref %s points nowhere", where, ref)
			return nil, ""
		}
	}
	b.problem("%s: a $ref goes round in a circle", where)
	return nil, ""
}

// readSchemes reads how apps log in. Every way but an API key is sent in
// the Authorization header; mutual TLS sends nothing the proxy can see.
func (b *builder) readSchemes() {
	schemes, _ := b.at("/components/securitySchemes")
	m, _ := schemes.(map[string]any)
	for name, v := range m {
		scheme, _ := b.object(v, "/components/securitySchemes"+token(name), b.name+" security scheme "+name)
		switch str(scheme, "type") {
		case "apiKey":
			b.schemes[name] = &credential{in: str(scheme, "in"), name: str(scheme, "name")}
		case "http", "oauth2", "openIdConnect":
			b.schemes[name] = &credential{in: "header", name: "Authorization"}
		}
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// checks builds the checks of one route. template is the route as the
// spec writes it, method in lower case.
func (b *builder) checks(template, method, where string) *checks {
	itemPointer := "/paths" + token(template)
	itemValue, _ := b.at(itemPointer)
	item, itemPointer := b.object(itemValue, itemPointer, where)
	opPointer := itemPointer + token(method)
	op, _ := item[method].(map[string]any)
	c := &checks{
		params:   b.params(where, item["parameters"], itemPointer+"/parameters", op["parameters"], opPointer+"/parameters"),
		security: b.security(op),
	}
	if v, ok := op["requestBody"]; ok {
		body, pointer := b.object(v, opPointer+"/requestBody", where)
		c.body = b.bodySpec(body, pointer, where, "the request body")
		if c.body != nil {
			c.body.required, _ = body["required"].(bool)
		}
	}
	if responses, ok := op["responses"].(map[string]any); ok {
		c.replies = map[string]*reply{}
		for code, v := range responses {
			r, pointer := b.object(v, opPointer+"/responses"+token(code), where)
			if len(code) == 3 && strings.HasSuffix(strings.ToUpper(code), "XX") {
				code = strings.ToUpper(code)
			}
			c.replies[code] = b.reply(r, pointer, where, code)
		}
	}
	b.clashes(c, where)
	return c
}

// params are the route's parameters: those of the path first, then the
// method's own, which win over a path one of the same name.
func (b *builder) params(where string, itemParams any, itemPointer string, opParams any, opPointer string) []param {
	var params []param
	for _, set := range []struct {
		list    any
		pointer string
	}{{itemParams, itemPointer}, {opParams, opPointer}} {
		list, _ := set.list.([]any)
		for i, v := range list {
			m, pointer := b.object(v, fmt.Sprintf("%s/%d", set.pointer, i), where)
			p := b.param(m, pointer, where)
			if p == nil {
				continue
			}
			same := slices.IndexFunc(params, func(q param) bool {
				return q.in == p.in && strings.EqualFold(q.name, p.name)
			})
			if same >= 0 {
				params[same] = *p
				continue
			}
			params = append(params, *p)
		}
	}
	return params
}

func (b *builder) param(m map[string]any, pointer, where string) *param {
	p := &param{name: str(m, "name"), in: str(m, "in"), explode: true}
	if p.name == "" || p.in == "" {
		return nil
	}
	if mark, marked := m["x-hotline-record"]; marked {
		p.record = b.headerMark(mark, p.in, p.name, where)
	}
	// OpenAPI says these headers are described elsewhere, so a header
	// parameter with their name is ignored.
	if p.in == partHeader && slices.Contains([]string{"accept", "content-type", "authorization"}, strings.ToLower(p.name)) {
		return nil
	}
	p.required, _ = m["required"].(bool)
	style := str(m, "style")
	p.deepObject = style == "deepObject"
	if explode, ok := m["explode"].(bool); ok {
		p.explode = explode
	} else if style != "" && style != "form" {
		p.explode = false
	}
	p.schema = schemaOf(m, pointer, p.in+" "+p.name)
	return p
}

// security is the method's own list of ways to log in, or the spec's.
func (b *builder) security(op map[string]any) [][]credential {
	v, ok := op["security"]
	if !ok {
		v, _ = b.at("/security")
	}
	list, _ := v.([]any)
	var ways [][]credential
	for _, item := range list {
		requirement, _ := item.(map[string]any)
		var way []credential
		for _, name := range slices.Sorted(maps.Keys(requirement)) {
			if cred := b.schemes[name]; cred != nil {
				way = append(way, *cred)
			}
		}
		if len(way) == 0 {
			// A way that needs nothing: the route is open to all.
			return nil
		}
		ways = append(ways, way)
	}
	return ways
}

func (b *builder) bodySpec(m map[string]any, pointer, where, what string) *bodySpec {
	listed, ok := m["content"].(map[string]any)
	if !ok {
		return nil
	}
	body := &bodySpec{}
	for _, mediaType := range slices.Sorted(maps.Keys(listed)) {
		media, _ := listed[mediaType].(map[string]any)
		mark, marked := media["x-hotline-body"]
		if marked && mark != "json" && mark != "xml" {
			b.problem("%s: x-hotline-body of %s %s must be json or xml", where, what, mediaType)
		}
		at := pointer + "/content" + token(mediaType)
		ct := content{
			mediaType: mediaType,
			json:      mark == "json",
			xmlMark:   mark == "xml",
			schema:    schemaOf(media, at, what+" "+mediaType),
		}
		schema, hasSchema := media["schema"]
		if hasSchema {
			ct.fields = b.fields(schema, at+"/schema")
		}
		// XML is read by the schema's shape, built only where a body can
		// be XML: by the mark, by its type, or by a wildcard type.
		if ct.xmlMark || isXML(strings.ToLower(mediaType)) || strings.Contains(mediaType, "*") {
			ct.xml = &xmlBody{}
			if hasSchema {
				ct.xml = b.xmlBody(schema, at+"/schema")
			}
		}
		body.content = append(body.content, ct)
	}
	return body
}

func (b *builder) reply(m map[string]any, pointer, where, code string) *reply {
	r := &reply{body: b.bodySpec(m, pointer, where, "the "+code+" reply")}
	headers, _ := m["headers"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		if strings.EqualFold(name, "Content-Type") {
			continue
		}
		h, hp := b.object(headers[name], pointer+"/headers"+token(name), where)
		if _, marked := h["x-hotline-record"]; marked {
			b.problem("%s: x-hotline-record on the %s reply header %s: a mark goes only on a header of the call", where, code, name)
		}
		required, _ := h["required"].(bool)
		r.headers = append(r.headers, param{
			name: name, in: partHeader, required: required,
			schema: schemaOf(h, hp, "the "+code+" reply header "+name),
		})
	}
	r.check = b.schemas.response(r, where)
	return r
}
