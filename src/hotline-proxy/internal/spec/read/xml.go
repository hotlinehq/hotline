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

// Read reads an XML body into the Go data a JSON body would give, with
// every value as text: a schema engine turns the text into the type the
// schema wants. It builds that data as it reads, with no tree of its own in
// between. The error is ErrNotXML or a *RootError. With no shape the value
// is nil.
func (x *XMLBody) Read(data []byte) (any, error) {
	r := xmlReader{d: xml.NewDecoder(bytes.NewReader(data))}
	root, err := r.root()
	if err != nil {
		return nil, err
	}
	if x.Root != "" && root.Name.Local != x.Root {
		return nil, &RootError{Want: x.Root}
	}
	var v any
	if x.Shape == nil {
		err = r.skip()
	} else {
		v, err = x.Shape.value(&r, root)
	}
	if err != nil {
		return nil, err
	}
	return v, r.end()
}

// xmlReader reads one XML document. A DOCTYPE is refused, so no entity is
// ever expanded, and nothing outside the body is ever fetched.
//
// It reads raw: names keep their prefix and no namespace is looked up,
// since only an element's name without its prefix is ever used. That
// saves the reader a copy of every name (Peter, 2026-11-06). The raw
// reader doesn't check that an element is closed by its own name, so open
// keeps the names of the open elements and checks it here.
type xmlReader struct {
	d    *xml.Decoder
	open []xml.Name
}

// next is the next piece of the document that matters: an element's start
// or end, or text. io.EOF ends the document; any other problem is
// ErrNotXML.
func (r *xmlReader) next() (xml.Token, error) {
	for {
		tok, err := r.d.RawToken()
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if err != nil {
			return nil, ErrNotXML
		}
		switch t := tok.(type) {
		case xml.StartElement:
			r.open = append(r.open, t.Name)
			return tok, nil
		case xml.EndElement:
			if len(r.open) == 0 || r.open[len(r.open)-1] != t.Name {
				return nil, ErrNotXML
			}
			r.open = r.open[:len(r.open)-1]
			return tok, nil
		case xml.CharData:
			return tok, nil
		case xml.Directive:
			return nil, ErrNotXML
		}
	}
}

// inside is the next piece inside an open element: a document that ends
// there is not XML.
func (r *xmlReader) inside() (xml.Token, error) {
	tok, err := r.next()
	if errors.Is(err, io.EOF) {
		return nil, ErrNotXML
	}
	return tok, err
}

// root reads up to the root element's start. Only space comes before it.
func (r *xmlReader) root() (xml.StartElement, error) {
	for {
		tok, err := r.inside()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, nil
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return xml.StartElement{}, ErrNotXML
			}
		}
	}
}

// end reads what comes after the root: only space.
func (r *xmlReader) end() error {
	for {
		tok, err := r.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if t, ok := tok.(xml.CharData); !ok || len(bytes.TrimSpace(t)) > 0 {
			return ErrNotXML
		}
	}
}

// skip reads the rest of an open element.
func (r *xmlReader) skip() error {
	for depth := 1; depth > 0; {
		tok, err := r.inside()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// text reads the rest of an open element: its own text, in one piece.
// Its child elements are read and left out.
func (r *xmlReader) text() (string, error) {
	var text []byte
	for {
		tok, err := r.inside()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			text = append(text, t...)
		case xml.StartElement:
			if err := r.skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			return string(text), nil
		}
	}
}

// loose reads the rest of an open element the schema says nothing about:
// its text when it has no child elements, or else an object of them, with
// a name met more than once read as a list.
func (r *xmlReader) loose() (any, error) {
	var text []byte
	var out map[string]any
	for {
		tok, err := r.inside()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.CharData:
			text = append(text, t...)
		case xml.EndElement:
			if out == nil {
				return string(text), nil
			}
			return out, nil
		case xml.StartElement:
			v, err := r.loose()
			if err != nil {
				return nil, err
			}
			if out == nil {
				out = map[string]any{}
			}
			name := t.Name.Local
			switch have := out[name].(type) {
			case nil:
				out[name] = v
			case []any:
				out[name] = append(have, v)
			default:
				out[name] = []any{have, v}
			}
		}
	}
}

// value reads the rest of the element start opened, by its shape. Text
// stays text.
func (s *XMLShape) value(r *xmlReader, start xml.StartElement) (any, error) {
	switch s.Kind {
	case Text:
		return r.text()
	case Object:
		return s.object(r, start)
	case Array:
		return s.list(r)
	}
	return r.loose()
}

func (s *XMLShape) object(r *xmlReader, start xml.StartElement) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range s.Props {
		if !p.Attribute {
			continue
		}
		for _, a := range start.Attr {
			if a.Name.Local == p.Name && a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
				out[p.Key] = a.Value
			}
		}
	}
	for {
		tok, err := r.inside()
		if err != nil {
			return nil, err
		}
		child, isStart := tok.(xml.StartElement)
		if _, isEnd := tok.(xml.EndElement); isEnd {
			return out, nil
		}
		if !isStart {
			continue
		}
		if err := s.child(r, child, out); err != nil {
			return nil, err
		}
	}
}

// child reads one child element of an object into out.
func (s *XMLShape) child(r *xmlReader, child xml.StartElement, out map[string]any) error {
	name := child.Name.Local
	p := s.prop(name)
	var err error
	switch {
	case p == nil:
		// An element the schema doesn't name is kept, so a schema that
		// forbids extras reports it, unless it would take the key of a
		// property read from another name.
		if _, set := out[name]; set || s.hasKey(name) {
			return r.skip()
		}
		out[name], err = r.loose()
	case p.Shape.Kind == Array && !p.Shape.Wrapped:
		var item any
		item, err = p.Shape.Item.value(r, child)
		list, _ := out[p.Key].([]any)
		out[p.Key] = append(list, item)
	default:
		if _, set := out[p.Key]; set {
			return r.skip()
		}
		out[p.Key], err = p.Shape.value(r, child)
	}
	return err
}

// hasKey says whether a property has the key.
func (s *XMLShape) hasKey(key string) bool {
	for _, p := range s.Props {
		if p.Key == key {
			return true
		}
	}
	return false
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

// list reads the rest of a wrapped list, by its items' name. With no name,
// every element is an item.
func (s *XMLShape) list(r *xmlReader) ([]any, error) {
	items := []any{}
	for {
		tok, err := r.inside()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return items, nil
		case xml.StartElement:
			if s.ItemName != "" && t.Name.Local != s.ItemName {
				err = r.skip()
				break
			}
			var item any
			item, err = s.Item.value(r, t)
			items = append(items, item)
		}
		if err != nil {
			return nil, err
		}
	}
}
