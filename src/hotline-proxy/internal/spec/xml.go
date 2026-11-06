package spec

import (
	"maps"
	"slices"
	"strings"

	"hotline-proxy/internal/spec/read"
)

// How an XML body is read is worked out here, from the schema and its
// OpenAPI xml notes; the reading itself is in the read package.

// xmlBody builds how a body schema reads XML. The root element is the one
// the schema's xml name gives, or else the name of the shared schema its
// $ref points to, as OpenAPI tools write it.
func (b *builder) xmlBody(schema any, pointer string) *read.XMLBody {
	raw, _ := schema.(map[string]any)
	x := &read.XMLBody{Shape: b.xmlShape(schema, pointer, map[string]*read.XMLShape{})}
	resolved, at := b.object(schema, pointer, b.name)
	component := ""
	if name, ok := strings.CutPrefix(at, "/components/schemas/"); ok && !strings.Contains(name, "/") {
		component = strings.NewReplacer("~1", "/", "~0", "~").Replace(name)
	}
	x.Root = elementName(raw, resolved, component)
	return x
}

// xmlShape builds the shape of the schema at pointer. A schema met again,
// through a $ref that goes round, shares its shape.
func (b *builder) xmlShape(v any, pointer string, seen map[string]*read.XMLShape) *read.XMLShape {
	raw, _ := v.(map[string]any)
	m, at := b.object(v, pointer, b.name)
	if m == nil {
		return &read.XMLShape{}
	}
	if s, ok := seen[at]; ok {
		return s
	}
	s := &read.XMLShape{}
	seen[at] = s
	types := kinds(raw, m)
	props, hasProps := m["properties"].(map[string]any)
	items, hasItems := m["items"]
	switch {
	case slices.Contains(types, "array") || hasItems && len(types) == 0:
		s.Kind = read.Array
		s.Wrapped, _ = notes(raw)["wrapped"].(bool)
		if !s.Wrapped {
			s.Wrapped, _ = notes(m)["wrapped"].(bool)
		}
		s.Item = b.xmlShape(items, at+"/items", seen)
		itemRaw, _ := items.(map[string]any)
		itemResolved, _ := b.object(items, at+"/items", b.name)
		s.ItemName = elementName(itemRaw, itemResolved, "")
	case slices.Contains(types, "object") || hasProps && len(types) == 0:
		s.Kind = read.Object
		for _, key := range slices.Sorted(maps.Keys(props)) {
			propPointer := at + "/properties" + token(key)
			propRaw, _ := props[key].(map[string]any)
			propResolved, _ := b.object(props[key], propPointer, b.name)
			attribute, _ := notes(propRaw)["attribute"].(bool)
			s.Props = append(s.Props, read.XMLProp{
				Key:       key,
				Name:      elementName(propRaw, propResolved, key),
				Attribute: attribute,
				Shape:     b.xmlShape(props[key], propPointer, seen),
			})
		}
	case len(types) > 0:
		s.Kind = read.Text
	}
	return s
}

// notes are a schema's OpenAPI xml notes.
func notes(m map[string]any) map[string]any {
	n, _ := m["xml"].(map[string]any)
	return n
}

// elementName is the name the xml notes give, the property's own first,
// or else fallback.
func elementName(raw, resolved map[string]any, fallback string) string {
	for _, m := range []map[string]any{raw, resolved} {
		if name, ok := notes(m)["name"].(string); ok && name != "" {
			return name
		}
	}
	return fallback
}
