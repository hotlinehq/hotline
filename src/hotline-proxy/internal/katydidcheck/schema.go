package katydidcheck

import (
	"encoding/json"
	"math"
	"strings"
)

type schema map[string]any

type resolver struct{ defs map[string]any }

func (r *resolver) deref(s schema) schema {
	for {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		n := ref[strings.LastIndex(ref, "/")+1:]
		t, _ := r.defs[n].(map[string]any)
		s = schema(t)
	}
}

func sub(s schema, k string) schema {
	m, _ := s[k].(map[string]any)
	return schema(m)
}

func (r *resolver) prop(s schema, name []byte) schema {
	s = r.deref(s)
	if p, ok := sub(s, "properties")[string(name)].(map[string]any); ok {
		return r.deref(schema(p))
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		l, _ := s[k].([]any)
		for _, m := range l {
			mm, _ := m.(map[string]any)
			if p := r.prop(schema(mm), name); p != nil {
				return p
			}
		}
	}
	return nil
}

func parseSchema(b []byte) (schema, map[string]any) {
	var s schema
	_ = json.Unmarshal(b, &s)
	d, _ := s["definitions"].(map[string]any)
	return s, d
}

func float64bits(f float64) uint64 { return math.Float64bits(f) }

// typeOf gives the schema's type name, or "" when it names none. A list of types gives its first non-null member.
// Schema choices (allOf, oneOf, anyOf) are read through their first member that names a type.
func (r *resolver) typeOf(s schema) string {
	s = r.deref(s)
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if str, ok := x.(string); ok && str != "null" {
				return str
			}
		}
	}
	if sub(s, "properties") != nil {
		return "object"
	}
	if s["items"] != nil {
		return "array"
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		l, _ := s[k].([]any)
		for _, m := range l {
			mm, _ := m.(map[string]any)
			if t := r.typeOf(schema(mm)); t != "" {
				return t
			}
		}
	}
	return ""
}

func (r *resolver) items(s schema) schema {
	s = r.deref(s)
	if m, ok := s["items"].(map[string]any); ok {
		return schema(m)
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		l, _ := s[k].([]any)
		for _, m := range l {
			mm, _ := m.(map[string]any)
			if it := r.items(schema(mm)); it != nil {
				return it
			}
		}
	}
	return nil
}
