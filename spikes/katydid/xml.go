package katydidcheck

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"go.katydid.org.za/parser-go/hedge"
	"go.katydid.org.za/parser-go/parse"
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

// toHedge turns an element into a hedge node tree shaped like what the JSON reader gives:
// objects are labelled children, arrays are children labelled 0,1,2..., scalars are typed by the schema.
func (r *resolver) value(n *xnode, s schema) hedge.Hedge {
	s = r.deref(s)
	switch s["type"] {
	case "object":
		return r.object(n, s)
	case "array":
		return nil // arrays are handled by the parent, which sees all repeated siblings
	case "integer":
		if i, err := strconv.ParseInt(strings.TrimSpace(n.text.String()), 10, 64); err == nil {
			return hedge.Hedge{hedge.NewInt64Node(i)}
		}
	case "number":
		if f, ok := parseFloat([]byte(strings.TrimSpace(n.text.String()))); ok {
			return hedge.Hedge{hedge.NewFloat64Node(f)}
		}
	case "boolean":
		switch strings.TrimSpace(n.text.String()) {
		case "true":
			return hedge.Hedge{hedge.NewTrueNode()}
		case "false":
			return hedge.Hedge{hedge.NewFalseNode()}
		}
	}
	return hedge.Hedge{hedge.NewStringNode(n.text.String())}
}

func (r *resolver) object(n *xnode, s schema) hedge.Hedge {
	var out hedge.Hedge
	done := map[string]bool{}
	for _, c := range n.children {
		if done[c.name] {
			continue
		}
		done[c.name] = true
		ps := r.prop(s, []byte(c.name))
		if ps["type"] == "array" {
			items := r.deref(sub(ps, "items"))
			var arr hedge.Hedge
			i := 0
			for _, d := range n.children {
				if d.name == c.name {
					arr = append(arr, hedge.Node{Label: hedge.NewInt64Token(int64(i)), Children: r.value(d, items)})
					i++
				}
			}
			out = append(out, hedge.Node{Label: hedge.NewStringToken(c.name), Children: hedge.Hedge{hedge.NewTagNode("array", arr...)}})
			continue
		}
		out = append(out, hedge.Node{Label: hedge.NewStringToken(c.name), Children: r.value(c, ps)})
	}
	return hedge.Hedge{hedge.NewTagNode("object", out...)}
}

func toHedge(data []byte, root schema, defs map[string]any) (hedge.Hedge, error) {
	n, err := readXML(data)
	if err != nil {
		return nil, err
	}
	r := &resolver{defs}
	return r.object(&xnode{children: n.children, name: n.name}, r.deref(root)), nil
}

func parseSchema(b []byte) (schema, map[string]any) {
	var s schema
	_ = json.Unmarshal(b, &s)
	d, _ := s["definitions"].(map[string]any)
	return s, d
}

// fixed wraps katydid's hedge parser, whose Token() returns an error on enter and leave
// (the JSON parser returns an unknown kind and no error), which makes the matcher refuse the tree.
type fixed struct {
	hedge.Parser
	last parse.Hint
}

func (f *fixed) Next() (parse.Hint, error) {
	h, err := f.Parser.Next()
	f.last = h
	return h, err
}

func (f *fixed) Token() (parse.Kind, []byte, error) {
	if f.last == parse.EnterHint || f.last == parse.LeaveHint {
		return parse.UnknownKind, nil, nil
	}
	return f.Parser.Token()
}

func newFixed(h hedge.Hedge) parse.Parser { return &fixed{Parser: hedge.NewParser(h)} }

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
