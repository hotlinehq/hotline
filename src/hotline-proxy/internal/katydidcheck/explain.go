package katydidcheck

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.katydid.org.za/parser-go/cast"
	"go.katydid.org.za/parser-go/parse"
)

// Failure is a ValidationError explained the way the proxy reports it today (see internal/spec/check.go):
// the place like $.Contents[1].Size, the broken rule by its JSON Schema keyword, and what the schema expects.
// For a missing field the place ends with the name of the missing field.
// Rule and Expected are empty when the failure is of a kind that is not explained yet (allOf, oneOf, anyOf, not, format).
type Failure struct {
	Place    string
	Rule     string
	Expected string
	// Where is the place katydid reported: the field names and list positions to the failing value or object.
	Where *ValidationError
}

func (f *Failure) Error() string {
	s := "invalid at " + f.Place
	if f.Rule != "" {
		s += ": " + f.Rule
		if f.Expected != "" {
			s += " " + f.Expected
		}
	}
	return s
}

func (f *Failure) Unwrap() error { return f.Where }

// explain works out the rule and the expected value from the schema and the body.
// It runs only for bodies that fail, and shares nothing between calls.
func (r *resolver) explain(root schema, p parse.Parser, where *ValidationError) error {
	tree, err := buildTree(p)
	if err != nil {
		return err
	}
	f := &Failure{Where: where}
	f.Place = placeOf(where.Path)
	s := r.deref(root)
	node := tree
	var parent schema
	var parentNode any
	for _, step := range where.Path {
		parent, parentNode = s, node
		switch st := step.(type) {
		case string:
			m, _ := node.(map[string]any)
			node = m[st]
			if s != nil {
				if forbidden(r.deref(s), st) {
					f.Rule = "false"
					return f
				}
				s = r.prop(s, []byte(st))
			}
		case int:
			l, _ := node.([]any)
			if st < len(l) {
				node = l[st]
			} else {
				node = nil
			}
			if s != nil {
				s = r.itemAt(s, st)
			}
		}
	}
	// Katydid points at the extra item or the extra field, santhosh at the list or at the object that holds it.
	if parent != nil && len(where.Path) > 0 {
		parent = r.deref(parent)
		switch last := where.Path[len(where.Path)-1].(type) {
		case int:
			if l, ok := parentNode.([]any); ok {
				if n, ok := limit(parent, "maxItems"); ok && len(l) > n {
					f.Place = placeOf(where.Path[:len(where.Path)-1])
					f.Rule, f.Expected = "maxItems", strconv.Itoa(n)
					return f
				}
			}
		case string:
			if m, ok := parentNode.(map[string]any); ok {
				if n, ok := limit(parent, "maxProperties"); ok && len(m) > n {
					f.Place = placeOf(where.Path[:len(where.Path)-1])
					f.Rule, f.Expected = "maxProperties", strconv.Itoa(n)
					return f
				}
			}
			if _, ok := sub(parent, "properties")[last]; !ok {
				if ap, ok := parent["additionalProperties"].(bool); ok && !ap {
					f.Rule = "additionalProperties"
					return f
				}
			}
		}
	}
	if s != nil {
		var missing string
		f.Rule, f.Expected, missing = r.judge(s, node)
		if missing != "" {
			f.Place += "." + missing
		}
	}
	return f
}

func placeOf(path []any) string {
	var b strings.Builder
	b.WriteString("$")
	for _, step := range path {
		switch st := step.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(st) + "]")
		case string:
			b.WriteString("." + st)
		}
	}
	return b.String()
}

// judge finds the first keyword of the schema that the node breaks.
// For required and additionalProperties it also gives the name of the field.
func (r *resolver) judge(s schema, node any) (rule, expected, name string) {
	s = r.deref(s)
	if rule, expected, name = r.judgeOwn(s, node); rule != "" {
		return rule, expected, name
	}
	// the members of allOf all apply, so the first one the node breaks is the answer
	members, _ := s["allOf"].([]any)
	for _, m := range members {
		mm, _ := m.(map[string]any)
		if rule, expected, name = r.judge(schema(mm), node); rule != "" {
			return rule, expected, name
		}
	}
	// the choices are one result, not one per choice
	for _, k := range []string{"oneOf", "anyOf"} {
		if s[k] != nil {
			return k, "", ""
		}
	}
	if s["not"] != nil {
		return "not", "", ""
	}
	return "", "", ""
}

// forbidden tells whether the schema writes false for the property: nothing is allowed there.
func forbidden(s schema, name string) bool {
	allowed, ok := sub(s, "properties")[name].(bool)
	return ok && !allowed
}

// judgeOwn finds the first keyword written in the schema itself that the node breaks.
func (r *resolver) judgeOwn(s schema, node any) (rule, expected, name string) {
	if want := wantTypes(s); len(want) > 0 && !slices.ContainsFunc(want, func(t string) bool { return isType(node, t) }) {
		return "type", strings.Join(want, " or "), ""
	}
	if c, ok := s["const"]; ok && !same(node, c) {
		return "const", fmt.Sprint(c), ""
	}
	if m, ok := node.(map[string]any); ok {
		if deps, _ := s["dependentRequired"].(map[string]any); deps != nil {
			for _, prop := range slices.Sorted(maps.Keys(deps)) {
				if _, present := m[prop]; !present {
					continue
				}
				needed, _ := deps[prop].([]any)
				for _, n := range needed {
					if n, _ := n.(string); n != "" {
						if _, there := m[n]; !there {
							return "dependentRequired", "", n
						}
					}
				}
			}
		}
		if req, _ := s["required"].([]any); req != nil {
			for _, n := range req {
				if n, _ := n.(string); n != "" {
					if _, there := m[n]; !there {
						return "required", "", n
					}
				}
			}
		}
		if ap, ok := s["additionalProperties"].(bool); ok && !ap {
			props := sub(s, "properties")
			var extra []string
			for k := range m {
				if _, known := props[k]; !known {
					extra = append(extra, k)
				}
			}
			if len(extra) > 0 {
				sort.Strings(extra)
				return "additionalProperties", "", extra[0]
			}
		}
		if n, ok := limit(s, "minProperties"); ok && len(m) < n {
			return "minProperties", strconv.Itoa(n), ""
		}
		if n, ok := limit(s, "maxProperties"); ok && len(m) > n {
			return "maxProperties", strconv.Itoa(n), ""
		}
	}
	if l, ok := node.([]any); ok {
		if n, ok := limit(s, "minItems"); ok && len(l) < n {
			return "minItems", strconv.Itoa(n), ""
		}
		if n, ok := limit(s, "maxItems"); ok && len(l) > n {
			return "maxItems", strconv.Itoa(n), ""
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		if !slices.ContainsFunc(enum, func(w any) bool { return same(node, w) }) {
			want := make([]string, len(enum))
			for i, w := range enum {
				want[i] = fmt.Sprint(w)
			}
			return "enum", strings.Join(want, ", "), ""
		}
	}
	if str, ok := node.(string); ok {
		if n, ok := limit(s, "minLength"); ok && utf8.RuneCountInString(str) < n {
			return "minLength", strconv.Itoa(n), ""
		}
		if n, ok := limit(s, "maxLength"); ok && utf8.RuneCountInString(str) > n {
			return "maxLength", strconv.Itoa(n), ""
		}
		if pat, ok := s["pattern"].(string); ok {
			if re, err := regexp.Compile(pat); err == nil && !re.MatchString(str) {
				return "pattern", pat, ""
			}
		}
	}
	if n, ok := node.(*big.Rat); ok {
		if w, ok := bound(s, "minimum"); ok && n.Cmp(w) < 0 {
			return "minimum", w.RatString(), ""
		}
		if w, ok := bound(s, "maximum"); ok && n.Cmp(w) > 0 {
			return "maximum", w.RatString(), ""
		}
		if w, ok := bound(s, "exclusiveMinimum"); ok && n.Cmp(w) <= 0 {
			return "exclusiveMinimum", w.RatString(), ""
		}
		if w, ok := bound(s, "exclusiveMaximum"); ok && n.Cmp(w) >= 0 {
			return "exclusiveMaximum", w.RatString(), ""
		}
		if w, ok := bound(s, "multipleOf"); ok && w.Sign() != 0 && !new(big.Rat).Quo(n, w).IsInt() {
			return "multipleOf", w.RatString(), ""
		}
	}
	// what is left of a string or a number that fails is its format: date-time, email, int32 and so on
	if f, _ := s["format"].(string); f != "" {
		if _, isText := node.(string); isText {
			return "format", f, ""
		}
		if _, isNumber := node.(*big.Rat); isNumber && (f == "int32" || f == "int64") {
			return "format", f, ""
		}
	}
	return "", "", ""
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

func isType(node any, t string) bool {
	switch t {
	case "null":
		return node == nil
	case "boolean":
		_, ok := node.(bool)
		return ok
	case "string":
		_, ok := node.(string)
		return ok
	case "number":
		_, ok := node.(*big.Rat)
		return ok
	case "integer":
		n, ok := node.(*big.Rat)
		return ok && n.IsInt()
	case "object":
		_, ok := node.(map[string]any)
		return ok
	case "array":
		_, ok := node.([]any)
		return ok
	}
	return true
}

// same compares a value of the body with a value decoded from the schema's own JSON.
func same(node any, want any) bool {
	switch w := want.(type) {
	case float64:
		n, ok := node.(*big.Rat)
		return ok && n.Cmp(new(big.Rat).SetFloat64(w)) == 0
	case string:
		n, ok := node.(string)
		return ok && n == w
	case bool:
		n, ok := node.(bool)
		return ok && n == w
	case nil:
		return node == nil
	}
	return false
}

func limit(s schema, key string) (int, bool) {
	f, ok := s[key].(float64)
	return int(f), ok
}

func bound(s schema, key string) (*big.Rat, bool) {
	f, ok := s[key].(float64)
	if !ok {
		return nil, false
	}
	return new(big.Rat).SetFloat64(f), true
}

// buildTree reads the whole body from a parser that tags objects and arrays (JSON Schema parsers, and the XML reader)
// into maps, lists and scalars. Numbers are *big.Rat. Only failing bodies are read this way.
func buildTree(p parse.Parser) (any, error) {
	h, err := p.Next()
	if err != nil {
		return nil, err
	}
	return buildFrom(p, h)
}

func buildFrom(p parse.Parser, h parse.Hint) (any, error) {
	switch h {
	case parse.ValueHint:
		kind, bs, err := p.Token()
		if err != nil {
			return nil, err
		}
		return scalarOf(kind, bs), nil
	case parse.EnterHint:
		return buildContainer(p)
	}
	return nil, errors.New("unexpected token while reading the body")
}

func buildContainer(p parse.Parser) (any, error) {
	var obj map[string]any
	var list []any
	var tagged any
	hasTag := false
	for {
		h, err := p.Next()
		if err != nil {
			return nil, err
		}
		switch h {
		case parse.LeaveHint:
			switch {
			case hasTag:
				return tagged, nil
			case list != nil:
				return list, nil
			case obj != nil:
				return obj, nil
			}
			return map[string]any{}, nil
		case parse.FieldHint:
			kind, bs, err := p.Token()
			if err != nil {
				return nil, err
			}
			name := ""
			index := -1
			switch kind {
			case parse.Int64Kind:
				var i int64
				cast.ToInt64Ptr(bs, &i)
				index = int(i)
			default:
				name = string(bs)
			}
			ch, err := p.Next()
			if err != nil {
				return nil, err
			}
			child, err := buildFrom(p, ch)
			if err != nil {
				return nil, err
			}
			switch {
			case kind == parse.TagKind:
				if m, empty := child.(map[string]any); empty && len(m) == 0 && name == "array" {
					child = []any{}
				}
				tagged, hasTag = child, true
			case index >= 0:
				for len(list) <= index {
					list = append(list, nil)
				}
				list[index] = child
			default:
				if obj == nil {
					obj = map[string]any{}
				}
				obj[name] = child
			}
		default:
			return nil, errors.New("unexpected token while reading the body")
		}
	}
}

func scalarOf(kind parse.Kind, bs []byte) any {
	switch kind {
	case parse.NullKind:
		return nil
	case parse.TrueKind:
		return true
	case parse.FalseKind:
		return false
	case parse.Int64Kind:
		var i int64
		cast.ToInt64Ptr(bs, &i)
		return new(big.Rat).SetInt64(i)
	case parse.Float64Kind:
		var u uint64
		cast.ToFloat64BitsPtr(bs, &u)
		return new(big.Rat).SetFloat64(math.Float64frombits(u))
	case parse.DecimalKind:
		if n, ok := new(big.Rat).SetString(string(bs)); ok {
			return n
		}
	}
	return string(bs)
}
