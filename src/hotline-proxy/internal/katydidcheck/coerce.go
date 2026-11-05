package katydidcheck

import (
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
)

// Types are the types the whole schema allows, following $ref. Nil when it names none.
func (p *Pool) Types() []string {
	c := p.take()
	defer p.give(c)
	r := &resolver{c.defs}
	return wantTypes(r.deref(c.root))
}

// Coerce turns text from a query, header, cookie or form into the number or true or false
// that the schema wants, so the schema can check it. v is changed in place and returned.
// Text that does not read as the wanted type is left as it is, for the schema to refuse.
func (p *Pool) Coerce(v any) any {
	c := p.take()
	defer p.give(c)
	r := &resolver{c.defs}
	return r.coerce(v, c.root)
}

func (r *resolver) coerce(v any, s schema) any {
	if s == nil {
		return v
	}
	s = r.deref(s)
	switch x := v.(type) {
	case string:
		return scalar(x, r.typesOf(s))
	case []any:
		prefix, _ := s["prefixItems"].([]any)
		for i := range x {
			item := r.items(s)
			if i < len(prefix) {
				m, _ := prefix[i].(map[string]any)
				item = schema(m)
			}
			x[i] = r.coerce(x[i], item)
		}
	case map[string]any:
		for key := range x {
			prop := r.prop(s, []byte(key))
			if prop == nil {
				extra, _ := s["additionalProperties"].(map[string]any)
				prop = schema(extra)
			}
			x[key] = r.coerce(x[key], prop)
		}
	}
	return v
}

// typesOf are the types of a schema, or of the first member of allOf, oneOf or anyOf that names some.
func (r *resolver) typesOf(s schema) []string {
	s = r.deref(s)
	if t := wantTypes(s); t != nil {
		return t
	}
	for _, k := range []string{"allOf", "oneOf", "anyOf"} {
		l, _ := s[k].([]any)
		for _, m := range l {
			mm, _ := m.(map[string]any)
			if t := r.typesOf(schema(mm)); t != nil {
				return t
			}
		}
	}
	return nil
}

func scalar(text string, want []string) any {
	switch {
	case slices.Contains(want, "integer") && isInteger(text),
		slices.Contains(want, "number") && isNumber(text):
		return json.Number(text)
	case slices.Contains(want, "boolean") && (text == "true" || text == "false"):
		return text == "true"
	}
	return text
}

func isInteger(text string) bool {
	_, ok := new(big.Int).SetString(text, 10)
	return ok
}

func isNumber(text string) bool {
	_, err := strconv.ParseFloat(text, 64)
	return err == nil
}

func wantTypes(s schema) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if x, ok := x.(string); ok {
				out = append(out, x)
			}
		}
		return out
	}
	return nil
}
