package spec

// What spec hands a schema engine: the shared schemas, and the plain
// description of each route and reply, with every OpenAPI $ref already
// turned into a JSON Schema one.

import (
	"maps"
	"slices"
	"strings"
)

// sharedPrefix starts a $ref to a shared schema of the spec, and defsPrefix
// the same $ref as the engine sees it.
const (
	sharedPrefix = "#/components/schemas/"
	defsPrefix   = "#/$defs/"
)

// schemaAt is a schema of the spec as it is written, and where it is.
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

// startEngine hands the shared schemas to the engine. On a problem, the
// builder's engine is nil, and no route is checked; the spec is refused.
func (b *builder) startEngine(newEngine NewEngine) {
	all, _ := b.at("/components/schemas")
	m, _ := all.(map[string]any)
	defs := make(map[string]Schema, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		s, ok := asSchema(m[name])
		if !ok {
			b.problem("%s: the schema %s cannot be used: a schema must be an object, or true or false", b.name, name)
			continue
		}
		defs[name] = s
	}
	engine, err := newEngine(defs)
	if err != nil {
		b.problem("%s cannot be read as JSON Schema: %v", b.name, err)
		return
	}
	b.engine = engine
}

// requestCheck builds the check of a route's request side.
func (b *builder) requestCheck(c *checks, segments []string, where string) RequestCheck {
	if b.engine == nil {
		return nil
	}
	r := RequestSpec{Params: b.paramSpecs(c.params, segments, where), Body: b.contents(c.body, where)}
	check, err := b.engine.Request(r)
	if err != nil {
		b.problem("%s: %v", where, err)
	}
	return check
}

// responseCheck builds the check of one reply.
func (b *builder) responseCheck(rp *reply, where string) ResponseCheck {
	if b.engine == nil {
		return nil
	}
	r := ResponseSpec{Headers: b.paramSpecs(rp.headers, nil, where), Body: b.contents(rp.body, where)}
	check, err := b.engine.Response(r)
	if err != nil {
		b.problem("%s: %v", where, err)
	}
	return check
}

func (b *builder) paramSpecs(params []param, segments []string, where string) []Param {
	out := make([]Param, len(params))
	for i, p := range params {
		out[i] = Param{
			Name: p.name, In: p.in, Required: p.required, Segment: -1,
			Explode: p.explode, DeepObject: p.deepObject,
			Schema: b.schema(p.schema, where),
			What:   p.schema.what,
		}
		if p.in == "path" {
			for at, segment := range segments {
				if isValue(segment) && valueName(segment) == p.name {
					out[i].Segment = at
					out[i].Rest = strings.HasSuffix(segment, "+}")
				}
			}
		}
	}
	return out
}

func (b *builder) contents(body *bodySpec, where string) []Content {
	if body == nil {
		return nil
	}
	out := make([]Content, len(body.content))
	for i, c := range body.content {
		out[i] = Content{Schema: b.schema(c.schema, where), XML: c.xml, What: c.schema.what}
		for _, f := range c.fields {
			out[i].Marked = append(out[i].Marked, Marked{Path: f.path, Name: f.name, Types: f.types})
		}
	}
	return out
}

// schema is a schema of the spec as the engine takes it, nil when the
// spec gives none.
func (b *builder) schema(at schemaAt, where string) Schema {
	if !at.found {
		return nil
	}
	s, ok := asSchema(at.value)
	if !ok {
		b.problem("%s: the schema of %s cannot be used: a schema must be an object, or true or false", where, at.what)
		return nil
	}
	return s
}

// asSchema copies a schema of the spec for the engine: true becomes {},
// false {"not": {}}, and each $ref to a shared schema names it under
// $defs. ok is false for a value that is no schema at all.
func asSchema(v any) (Schema, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return Schema{}, true
		}
		return Schema{"not": map[string]any{}}, true
	case map[string]any:
		return copyRefs(x).(map[string]any), true
	}
	return nil, false
}

// copyRefs copies JSON data, turning each $ref to a shared schema of the
// spec into one under $defs. Other $refs are kept as written, and the
// engine finds them broken.
func copyRefs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, child := range x {
			if ref, isText := child.(string); key == "$ref" && isText {
				if name, shared := strings.CutPrefix(ref, sharedPrefix); shared {
					child = defsPrefix + name
				}
			}
			out[key] = copyRefs(child)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = copyRefs(child)
		}
		return out
	}
	return v
}
