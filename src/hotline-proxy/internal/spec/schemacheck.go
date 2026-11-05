package spec

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.katydid.org.za/validator-go-jsonschema/jsonschema/allowlist"

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

// schemaOf makes the check of the schema v found at pointer. The check holds
// the schema and only the shapes it reaches by $ref, as plain JSON Schema with
// the shapes under $defs. Loading all of a big spec's shapes into each check
// would cost far more than the check needs. The same schema is made once. A
// keyword the checker does not support is an error, so a spec that uses one is
// refused at load, never silently left unchecked.
func (b *builder) schemaOf(v any, pointer string) (*schemaCheck, error) {
	root, defs, err := b.closure(v, pointer)
	if err != nil {
		return nil, err
	}
	if len(defs) > 0 {
		root["$defs"] = defs
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(data)
	if c, ok := b.checkers[key]; ok {
		return c, nil
	}
	if err := allowlist.Check(data); err != nil {
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

// closure copies the schema v, with every $ref pointing at a shape under
// $defs, and returns those shapes. A $ref to another file, or to nothing, is
// an error.
func (b *builder) closure(v any, pointer string) (map[string]any, map[string]any, error) {
	defs := map[string]any{}
	var failure error
	var rewrite func(any) any
	rewrite = func(x any) any {
		switch x := x.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for key, child := range x {
				if text, isText := child.(string); isText {
					switch key {
					case "$ref":
						out[key] = "#/$defs/" + b.shape(text, defs, rewrite, &failure)
						continue
					case "format":
						if !checkedFormats[text] {
							continue
						}
					}
				}
				out[key] = rewrite(child)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, child := range x {
				out[i] = rewrite(child)
			}
			return out
		}
		return x
	}
	root, _ := rewrite(v).(map[string]any)
	if root == nil {
		if failure == nil {
			failure = fmt.Errorf("the schema at %s is not an object", pointer)
		}
	}
	return root, defs, failure
}

// shape finds the name under $defs of the shape a $ref points at, and adds the
// shape to defs the first time it is met.
func (b *builder) shape(ref string, defs map[string]any, rewrite func(any) any, failure *error) string {
	if !strings.HasPrefix(ref, "#") {
		if *failure == nil {
			*failure = fmt.Errorf("the $ref %s points to another file: %w", ref, errOutside)
		}
		return "missing"
	}
	pointer, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil {
		pointer = strings.TrimPrefix(ref, "#")
	}
	name, known := b.shapeNames[pointer]
	if known {
		return name
	}
	name = fmt.Sprintf("s%d", len(b.shapeNames)+1)
	b.shapeNames[pointer] = name
	target, found := b.at(pointer)
	if !found {
		if *failure == nil {
			*failure = fmt.Errorf("the $ref %s points nowhere", ref)
		}
		return name
	}
	defs[name] = nil // keeps a shape that refers to itself from being followed again
	defs[name] = rewrite(target)
	return name
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
