package santhosh

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"hotline-proxy/internal/spec"
)

// Schema is one compiled schema of the spec, a spec.Schema.
type Schema struct {
	s *jsonschema.Schema
}

// Coerce turns text from a query, header, cookie or form into the number
// or true/false the schema wants, so the schema can check it. Lists and
// objects are changed in place.
func (s *Schema) Coerce(v any) any {
	return coerce(v, s.s)
}

// Types are the types the schema allows, following $ref.
func (s *Schema) Types() []string {
	return types(s.s)
}

// Validate checks a value and says what is wrong with it, sorted by
// place. base names the value: a parameter's name, or $ for a body.
func (s *Schema) Validate(v any, base string) []spec.Failure {
	return validate(s.s, v, base)
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

// coerce turns text from a query, header, cookie or form into the number
// or true/false the schema wants, so the schema can check it.
func coerce(v any, s *jsonschema.Schema) any {
	if s == nil {
		return v
	}
	for s.Ref != nil && s.Types == nil && s.Properties == nil && s.Items2020 == nil {
		s = s.Ref
	}
	switch x := v.(type) {
	case string:
		return scalar(x, types(s))
	case []any:
		for i := range x {
			item := s.Items2020
			if i < len(s.PrefixItems) {
				item = s.PrefixItems[i]
			}
			x[i] = coerce(x[i], item)
		}
	case map[string]any:
		for key := range x {
			prop := s.Properties[key]
			if extra, ok := s.AdditionalProperties.(*jsonschema.Schema); ok && prop == nil {
				prop = extra
			}
			x[key] = coerce(x[key], prop)
		}
	}
	return v
}

// types are the types a schema allows, following $ref.
func types(s *jsonschema.Schema) []string {
	for ; s != nil; s = s.Ref {
		if s.Types != nil {
			return s.Types.ToStrings()
		}
	}
	return nil
}

// validate checks a value against a schema and turns what is wrong into
// failures. base names the value: a parameter's name, or $ for a body.
func validate(s *jsonschema.Schema, instance any, base string) []spec.Failure {
	var invalid *jsonschema.ValidationError
	if !errors.As(s.Validate(instance), &invalid) {
		return nil
	}
	var leaves []*jsonschema.ValidationError
	collect(invalid, &leaves)
	var results []spec.Failure
	for _, e := range leaves {
		place := placeOf(base, instance, e.InstanceLocation)
		switch k := e.ErrorKind.(type) {
		case *kind.Required:
			for _, name := range k.Missing {
				results = append(results, spec.Failure{Place: place + "." + name, Rule: "required"})
			}
		case *kind.AdditionalProperties:
			for _, name := range k.Properties {
				results = append(results, spec.Failure{Place: place + "." + name, Rule: "additionalProperties"})
			}
		case *kind.DependentRequired:
			for _, name := range k.Missing {
				results = append(results, spec.Failure{Place: place + "." + name, Rule: "dependentRequired"})
			}
		case *kind.ContentMediaType:
			// A string that should hold JSON doesn't.
			results = append(results, spec.Failure{Place: place, Rule: "json"})
		case *kind.ContentEncoding:
			// A string that should be base64 text isn't.
			results = append(results, spec.Failure{Place: place, Rule: "base64"})
		default:
			results = append(results, spec.Failure{Place: place, Rule: rule(e.ErrorKind), Expected: expected(e.ErrorKind)})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Place < results[j].Place })
	return results
}

// rule names the broken rule by its JSON Schema keyword.
func rule(k jsonschema.ErrorKind) string {
	switch k.(type) {
	case *kind.Not:
		return "not"
	case *kind.FalseSchema:
		return "false"
	}
	path := append([]string{"schema"}, k.KeywordPath()...)
	return path[len(path)-1]
}

// collect finds the errors that say what is wrong, leaving out those that
// only group others. For anyOf, oneOf and not the error itself is kept:
// the errors of each choice would only confuse.
func collect(e *jsonschema.ValidationError, leaves *[]*jsonschema.ValidationError) {
	switch e.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf, *kind.Not:
		*leaves = append(*leaves, e)
		return
	}
	if len(e.Causes) == 0 {
		*leaves = append(*leaves, e)
		return
	}
	for _, cause := range e.Causes {
		collect(cause, leaves)
	}
}

// placeOf writes where a value sits: $.lines[1].sku in a body, tag[1] or
// filter.status in a parameter.
func placeOf(base string, instance any, location []string) string {
	var b strings.Builder
	b.WriteString(base)
	for _, token := range location {
		if token == insideJSON {
			// The value is JSON text; the place carries on inside it.
			if text, ok := instance.(string); ok {
				instance, _ = jsonschema.UnmarshalJSON(strings.NewReader(text))
			}
			continue
		}
		if list, ok := instance.([]any); ok {
			i, _ := strconv.Atoi(token)
			b.WriteString("[" + token + "]")
			instance = list[i]
			continue
		}
		b.WriteString("." + token)
		if m, ok := instance.(map[string]any); ok {
			instance = m[token]
		}
	}
	return b.String()
}

// expected is what the spec asks for, taken from the schema, never from
// the value.
func expected(k jsonschema.ErrorKind) string {
	switch k := k.(type) {
	case *kind.Type:
		return strings.Join(k.Want, " or ")
	case *kind.Format:
		return k.Want
	case *kind.Enum:
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = fmt.Sprint(w)
		}
		return strings.Join(want, ", ")
	case *kind.Const:
		return fmt.Sprint(k.Want)
	case *kind.Pattern:
		return k.Want
	case *kind.Minimum:
		return k.Want.RatString()
	case *kind.Maximum:
		return k.Want.RatString()
	case *kind.ExclusiveMinimum:
		return k.Want.RatString()
	case *kind.ExclusiveMaximum:
		return k.Want.RatString()
	case *kind.MultipleOf:
		return k.Want.RatString()
	case *kind.MinLength:
		return strconv.Itoa(k.Want)
	case *kind.MaxLength:
		return strconv.Itoa(k.Want)
	case *kind.MinItems:
		return strconv.Itoa(k.Want)
	case *kind.MaxItems:
		return strconv.Itoa(k.Want)
	case *kind.MinProperties:
		return strconv.Itoa(k.Want)
	case *kind.MaxProperties:
		return strconv.Itoa(k.Want)
	}
	return ""
}
