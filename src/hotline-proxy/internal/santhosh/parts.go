package santhosh

import (
	"net/url"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"hotline-proxy/internal/spec"
	"hotline-proxy/internal/spec/read"
)

func (q *requestCheck) TextPart(contentIndex spec.ContentIndex, property string, parts []spec.FormPart) ([]spec.Finding, []spec.Field) {
	values := url.Values{}
	for _, p := range parts {
		values.Add(p.Name, string(p.Value))
	}
	place := whole + "." + property
	form, err := read.FormValues(values)
	if err != nil {
		return []spec.Finding{{Part: partBody, Place: place, Rule: "form"}}, nil
	}
	c := q.body[contentIndex]
	value := form[property]
	schema := c.propertySchema(property)
	value = coerce(value, schema)
	fields := c.propertyFields(property, value)
	if schema == nil {
		return nil, fields
	}
	return validate(schema, c.notes, value, partBody, place), fields
}

func (q *requestCheck) JSONPart(contentIndex spec.ContentIndex, property string, data []byte) ([]spec.Finding, []spec.Field) {
	place := whole + "." + property
	value, err := decodeJSON(data)
	if err != nil {
		return []spec.Finding{{Part: partBody, Place: place, Rule: "json"}}, nil
	}
	c := q.body[contentIndex]
	fields := c.propertyFields(property, value)
	schema := c.propertySchema(property)
	if schema == nil {
		return nil, fields
	}
	return validate(schema, c.notes, value, partBody, place), fields
}

func (q *requestCheck) FormPartNames(contentIndex spec.ContentIndex, came []string) []spec.Finding {
	c := q.body[contentIndex]
	if c.schema == nil {
		return nil
	}
	// The names are checked as an object holding each one; its values are
	// checked part by part, so only nameRules, the rules about which names
	// an object holds, are kept.
	nameRules := []string{"required", "additionalProperties", "propertyNames", "minProperties", "maxProperties", "dependentRequired"}
	object := make(map[string]any, len(came))
	for _, name := range came {
		object[name] = nil
	}
	var findings []spec.Finding
	for _, f := range validate(c.schema, c.notes, object, partBody, whole) {
		name, inside := strings.CutPrefix(f.Place, whole+".")
		if slices.Contains(nameRules, f.Rule) && (f.Place == whole || inside && !strings.Contains(name, ".")) {
			findings = append(findings, f)
		}
	}
	return findings
}

// propertySchema is the compiled schema of one property of the content,
// nil when its schema names none. It looks through $ref and allOf, the
// ways a schema is put together from others,
// each schema once.
func (c content) propertySchema(property string) *jsonschema.Schema {
	todo := []*jsonschema.Schema{c.schema}
	seen := map[*jsonschema.Schema]bool{}
	for len(todo) > 0 {
		s := todo[0]
		todo = todo[1:]
		if s == nil || seen[s] {
			// A schema can name itself, through a $ref back to it.
			continue
		}
		seen[s] = true
		if found, ok := s.Properties[property]; ok {
			return found
		}
		todo = append(append(todo, s.Ref), s.AllOf...)
	}
	return nil
}

// propertyFields are the marked fields read from one property's value.
func (c content) propertyFields(property string, value any) []spec.Field {
	var fields []spec.Field
	for _, m := range c.Marked {
		if m.Path[0] != property {
			continue
		}
		if plain, ok := read.Plain(read.At(value, m.Path[1:]), m.Types); ok {
			fields = append(fields, spec.Field{Name: m.Name, Value: plain})
		}
	}
	return fields
}
