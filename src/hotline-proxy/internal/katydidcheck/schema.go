package katydidcheck

import (
	"encoding/json"
	"math"
	"strings"
)

type schema map[string]any

type resolver struct{ defs map[string]any }

// deref follows $ref. The keywords written beside the $ref are kept: the result holds those of the target and those beside the $ref.
func (r *resolver) deref(s schema) schema {
	for range 32 {
		ref, ok := s["$ref"].(string)
		if !ok {
			return s
		}
		n := ref[strings.LastIndex(ref, "/")+1:]
		t, _ := r.defs[n].(map[string]any)
		merged := make(schema, len(t)+len(s))
		for k, v := range t {
			merged[k] = v
		}
		for k, v := range s {
			if k != "$ref" {
				merged[k] = v
			}
		}
		s = merged
	}
	return s
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
	if extra, ok := s["additionalProperties"].(map[string]any); ok {
		return r.deref(schema(extra))
	}
	return nil
}

// itemAt is the schema of the list item at a position: prefixItems first, then items.
func (r *resolver) itemAt(s schema, i int) schema {
	s = r.deref(s)
	if prefix, ok := s["prefixItems"].([]any); ok && i < len(prefix) {
		m, _ := prefix[i].(map[string]any)
		return schema(m)
	}
	for _, k := range []string{"allOf"} {
		l, _ := s[k].([]any)
		for _, m := range l {
			mm, _ := m.(map[string]any)
			if it := r.itemAt(schema(mm), i); it != nil {
				return it
			}
		}
	}
	return r.items(s)
}

func parseSchema(b []byte) (schema, map[string]any) {
	var s schema
	_ = json.Unmarshal(b, &s)
	d, _ := s["definitions"].(map[string]any)
	if d == nil {
		d, _ = s["$defs"].(map[string]any)
	}
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
