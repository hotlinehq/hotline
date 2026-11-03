package katydidcheck

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"go.katydid.org.za/parser-go/hedge"
	"go.katydid.org.za/parser-go/parse"
)

// xnode is a plain element tree read with Go's standard XML reader.
type xnode struct {
	name     string
	text     strings.Builder
	children []*xnode
}

func readXML(data []byte) (*xnode, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var stack []*xnode
	var root *xnode
	for {
		t, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				c := &xnode{name: a.Name.Local}
				c.text.WriteString(a.Value)
				n.children = append(n.children, c)
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else if root == nil {
				root = n
			} else {
				return nil, errors.New("two roots")
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.Directive:
			return nil, errors.New("doctype refused")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("not well formed")
	}
	return root, nil
}

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

func (r *resolver) prop(s schema, name string) schema {
	p, _ := sub(s, "properties")[name].(map[string]any)
	return r.deref(schema(p))
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
		if f, err := strconv.ParseFloat(strings.TrimSpace(n.text.String()), 64); err == nil {
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
		ps := r.prop(s, c.name)
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
