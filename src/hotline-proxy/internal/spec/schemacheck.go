package spec

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"hotline-proxy/internal/katydidcheck"
)

// errOutside marks a $ref to a document other than the spec.
var errOutside = errors.New("outside the spec")

// checkedFormats are the formats the proxy checks as rules: date-time, date,
// email, uuid, uri, ipv4 and ipv6. Any other format is left out of the
// schema, since katydid would check it too (Peter, 2026-10-29).
var checkedFormats = map[string]bool{
	"date-time": true, "date": true, "email": true, "uuid": true, "uri": true, "ipv4": true, "ipv6": true,
}

// schemaCheck checks values against one schema of the spec. It is safe to use
// from many goroutines at once.
type schemaCheck struct {
	pool *katydidcheck.Pool
}

// shape is a part of the spec that a $ref points at, written as plain JSON
// Schema with its own $ref rewritten to point under $defs. It is made once,
// and shared by every check that reaches it.
type shape struct {
	name  string
	value any
	// reaches are the pointers of the shapes this one refers to.
	reaches []string
}

// schemaOf makes the check of the schema v found at pointer. The check holds
// the schema and only the shapes it reaches by $ref, as plain JSON Schema
// with the shapes under $defs. Loading all of a big spec's shapes into each
// check would cost far more than the check needs. The same schema is made
// once. A keyword the checker does not support is an error, so a spec that
// uses one is refused at load, never silently left unchecked.
func (b *builder) schemaOf(v any, pointer string) (*schemaCheck, error) {
	if target, ok := onlyRef(v); ok {
		return b.shapeCheck(target)
	}
	var reaches []string
	root, err := b.rewrite(v, &reaches)
	if err != nil {
		return nil, err
	}
	return b.checkOf(root, reaches)
}

// shapeCheck is the check of a shape a $ref points at.
func (b *builder) shapeCheck(pointer string) (*schemaCheck, error) {
	if c, ok := b.byShape[pointer]; ok {
		return c.check, c.err
	}
	s, err := b.shape(pointer)
	var c *schemaCheck
	if err == nil {
		c, err = b.checkOf(s.value, s.reaches)
	}
	b.byShape[pointer] = shapeResult{c, err}
	return c, err
}

type shapeResult struct {
	check *schemaCheck
	err   error
}

// checkOf makes the check of a rewritten schema and the shapes it reaches.
func (b *builder) checkOf(root any, reaches []string) (*schemaCheck, error) {
	doc, ok := root.(map[string]any)
	if !ok {
		return nil, errNotObject
	}
	defs := map[string]any{}
	seen := map[string]bool{}
	var walk func(pointers []string) error
	walk = func(pointers []string) error {
		for _, p := range pointers {
			if seen[p] {
				continue
			}
			seen[p] = true
			s, err := b.shape(p)
			if err != nil {
				return err
			}
			defs[s.name] = s.value
			if err := walk(s.reaches); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(reaches); err != nil {
		return nil, err
	}
	if len(defs) > 0 {
		doc = maps.Clone(doc)
		doc["$defs"] = defs
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(data)
	if c, ok := b.checkers[key]; ok {
		return c, nil
	}
	if err := b.allowed.Supported(data); err != nil {
		return nil, err
	}
	pool, err := katydidcheck.NewPool(data)
	if err != nil {
		return nil, err
	}
	c := &schemaCheck{pool: pool}
	b.checkers[key] = c
	return c, nil
}

var errNotObject = errors.New("the schema is not an object")

// shape finds the shape at a pointer into the spec, made the first time it is
// asked for. A $ref to another file, or to nothing, is an error.
func (b *builder) shape(pointer string) (*shape, error) {
	if s, ok := b.shapes[pointer]; ok {
		return s, nil
	}
	target, found := b.at(pointer)
	if !found {
		return nil, fmt.Errorf("the $ref #%s points nowhere", pointer)
	}
	s := &shape{name: fmt.Sprintf("s%d", len(b.shapes)+1)}
	b.shapes[pointer] = s // so a shape that refers to itself is not made again
	value, err := b.rewrite(target, &s.reaches)
	if err != nil {
		delete(b.shapes, pointer)
		return nil, err
	}
	s.value = value
	return s, nil
}

// rewrite copies the schema x, with every $ref pointing under $defs, and
// adds the pointers it reaches to reaches. Formats the proxy does not check
// are left out.
func (b *builder) rewrite(x any, reaches *[]string) (any, error) {
	switch x := x.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, child := range x {
			if text, isText := child.(string); isText {
				switch key {
				case "$ref":
					if !strings.HasPrefix(text, "#") {
						return nil, fmt.Errorf("the $ref %s points to another file: %w", text, errOutside)
					}
					pointer, err := url.PathUnescape(strings.TrimPrefix(text, "#"))
					if err != nil {
						pointer = strings.TrimPrefix(text, "#")
					}
					s, err := b.shape(pointer)
					if err != nil {
						return nil, err
					}
					*reaches = append(*reaches, pointer)
					out[key] = "#/$defs/" + s.name
					continue
				case "format":
					if !checkedFormats[text] {
						continue
					}
				}
			}
			value, err := b.rewrite(child, reaches)
			if err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			value, err := b.rewrite(child, reaches)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	}
	return x, nil
}

// coerce turns text from a query, header, cookie or form into the number or
// true/false the schema wants, so the schema can check it.
func (s *schemaCheck) coerce(v any) any {
	if s == nil {
		return v
	}
	return s.pool.Coerce(v)
}

// types are the types the schema allows, following $ref.
func (s *schemaCheck) types() []string {
	if s == nil {
		return nil
	}
	return s.pool.Types()
}

// validate checks a value against the schema and turns what is wrong into
// results. base names the value: a parameter's name, or $ for a body. The
// checker reports the first failure only.
func (s *schemaCheck) validate(instance any, part, base string) []Result {
	data, err := json.Marshal(instance)
	if err != nil {
		return []Result{{Part: part, Place: base, Rule: "json"}}
	}
	return s.validateJSON(data, part, base)
}

func (s *schemaCheck) validateJSON(data []byte, part, base string) []Result {
	err := s.pool.WhyJSON(data)
	if err == nil {
		return nil
	}
	var failure *katydidcheck.Failure
	if !errors.As(err, &failure) {
		return []Result{{Part: part, Place: base, Rule: "json"}}
	}
	return []Result{{Part: part, Place: base + strings.TrimPrefix(failure.Place, "$"), Rule: failure.Rule, Expected: failure.Expected}}
}

// onlyRef is the pointer of the shape v names, when v holds nothing but a
// $ref within the spec.
func onlyRef(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	ref, ok := m["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#") {
		return "", false
	}
	target, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	return target, err == nil
}
