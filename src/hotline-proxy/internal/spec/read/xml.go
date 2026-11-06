package read

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// ErrNotXML marks a body that is not one well-formed XML document the
// proxy is willing to read.
var ErrNotXML = errors.New("not readable XML")

// RootError marks an XML body whose root is not the element the schema
// names.
type RootError struct {
	Want string
}

func (e *RootError) Error() string {
	return "the root element is not " + e.Want
}

// What a schema says an XML element holds.
const (
	// Any is a schema that names no type: the element is read as it
	// comes, as text, or as an object of its child elements.
	Any = iota
	Text
	Object
	Array
)

// XMLShape is what a schema and its OpenAPI xml notes say about an XML
// element, so the element can be read into the Go data a JSON body would
// give.
type XMLShape struct {
	Kind  int
	Props []XMLProp
	// Item is the shape of a list's items, and ItemName their element's
	// name.
	Item     *XMLShape
	ItemName string
	// Wrapped says the list has an outer element around its items.
	Wrapped bool
}

// XMLProp is one property of an object: the key it has in the Go data,
// the element or attribute it is read from, and its shape.
type XMLProp struct {
	Key       string
	Name      string
	Attribute bool
	Shape     *XMLShape
}

// XMLBody is how an XML body is read: the root element's name the schema
// expects (empty when it names none) and the root's shape (nil when the
// spec gives no schema, so the body is only checked to be XML).
type XMLBody struct {
	Root  string
	Shape *XMLShape
}

// IsXML says whether a media type is one of XML's.
func IsXML(mediaType string) bool {
	return mediaType == "application/xml" || mediaType == "text/xml" || strings.HasSuffix(mediaType, "+xml")
}

// element is one element of a read XML body.
type element struct {
	name     string
	attrs    map[string]string
	text     strings.Builder
	children []*element
}

// Read reads an XML body into the Go data a JSON body would give, with
// every value as text: a schema engine turns the text into the type the
// schema wants. The error is ErrNotXML or a *RootError. With no shape the
// value is nil.
func (x *XMLBody) Read(data []byte) (any, error) {
	root, err := parseXML(data)
	if err != nil {
		return nil, err
	}
	if x.Root != "" && root.name != x.Root {
		return nil, &RootError{Want: x.Root}
	}
	if x.Shape == nil {
		return nil, nil //nolint:nilnil // a body that is XML, with no shape to read it by
	}
	return x.Shape.value(root), nil
}

// parseXML reads one XML document. A DOCTYPE is refused, so no entity is
// ever expanded, and nothing outside the body is ever fetched.
func parseXML(data []byte) (*element, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var root *element
	var open []*element
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrNotXML
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{name: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					e.attrs[a.Name.Local] = a.Value
				}
			}
			switch {
			case len(open) > 0:
				parent := open[len(open)-1]
				parent.children = append(parent.children, e)
			case root != nil:
				return nil, ErrNotXML
			default:
				root = e
			}
			open = append(open, e)
		case xml.EndElement:
			open = open[:len(open)-1]
		case xml.CharData:
			if len(open) > 0 {
				open[len(open)-1].text.Write(t)
			} else if len(bytes.TrimSpace(t)) > 0 {
				return nil, ErrNotXML
			}
		case xml.Directive:
			return nil, ErrNotXML
		}
	}
	if root == nil || len(open) > 0 {
		return nil, ErrNotXML
	}
	return root, nil
}

// value reads an element by its shape. Text stays text.
func (s *XMLShape) value(e *element) any {
	switch s.Kind {
	case Text:
		return e.text.String()
	case Object:
		return s.object(e)
	case Array:
		return s.list(e.children, s.ItemName)
	}
	return loose(e)
}

func (s *XMLShape) object(e *element) map[string]any {
	out := map[string]any{}
	keys := map[string]bool{}
	for _, p := range s.Props {
		keys[p.Key] = true
		if !p.Attribute {
			continue
		}
		if v, ok := e.attrs[p.Name]; ok {
			out[p.Key] = v
		}
	}
	for _, child := range e.children {
		p := s.prop(child.name)
		switch {
		case p == nil:
			// An element the schema doesn't name is kept, so a schema that
			// forbids extras reports it, unless it would take the key of a
			// property read from another name.
			if _, set := out[child.name]; !set && !keys[child.name] {
				out[child.name] = loose(child)
			}
		case p.Shape.Kind == Array && !p.Shape.Wrapped:
			list, _ := out[p.Key].([]any)
			out[p.Key] = append(list, p.Shape.Item.value(child))
		default:
			if _, set := out[p.Key]; !set {
				out[p.Key] = p.Shape.value(child)
			}
		}
	}
	return out
}

// prop is the property read from the element named name. An unwrapped
// list's items carry the item's name.
func (s *XMLShape) prop(name string) *XMLProp {
	for i, p := range s.Props {
		if p.Attribute {
			continue
		}
		want := p.Name
		if p.Shape.Kind == Array && !p.Shape.Wrapped {
			want = p.Shape.ItemName
			if want == "" {
				want = p.Name
			}
		}
		if want == name {
			return &s.Props[i]
		}
	}
	return nil
}

// list reads the items of a list among elements, by the items' name. With
// no name, every element is an item.
func (s *XMLShape) list(elements []*element, name string) []any {
	items := []any{}
	for _, e := range elements {
		if name == "" || e.name == name {
			items = append(items, s.Item.value(e))
		}
	}
	return items
}

// loose reads an element the schema says nothing about: its text when it
// has no child elements, or else an object of them, with a name met more
// than once read as a list.
func loose(e *element) any {
	if len(e.children) == 0 {
		return e.text.String()
	}
	out := map[string]any{}
	for _, child := range e.children {
		v := loose(child)
		switch have := out[child.name].(type) {
		case nil:
			out[child.name] = v
		case []any:
			out[child.name] = append(have, v)
		default:
			out[child.name] = []any{have, v}
		}
	}
	return out
}
