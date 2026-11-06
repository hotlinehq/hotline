// Package santhosh checks calls against JSON Schemas with the
// santhosh-tekuri library, as a spec.Engine. It is the only package that
// knows that library; config hands NewEngine to spec.Parse. It knows
// nothing of OpenAPI: spec hands it plain JSON Schemas.
package santhosh

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/url"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"hotline-proxy/internal/spec"
)

// base is where the schemas live inside the JSON Schema compiler. Nothing
// is ever fetched from it.
const base = "https://schemas.hotline.invalid"

// defsURL is the address of the document holding the shared schemas,
// under $defs.
const defsURL = base + "/defs.json"

// errOutside marks a $ref to a document other than the shared schemas.
var errOutside = errors.New("outside the spec")

// errBits marks a whole number too large for its format.
var errBits = errors.New("does not fit")

// Engine compiles the schemas of one spec and builds the checks of its
// routes. It is used while a spec is loaded, by one goroutine.
type Engine struct {
	compiler *jsonschema.Compiler
	// inline compiles schemas that need nothing else, such as
	// { type: string } on a header, apart from the shared ones, and
	// inlined keeps each one by its text, so the same schema is compiled
	// once.
	inline  *jsonschema.Compiler
	inlined map[string]*jsonschema.Schema
	// next numbers the schemas given an address of their own.
	next int
}

// outside refuses every document but the shared schemas, so a $ref to
// another file or address is a problem.
type outside struct{}

func (outside) Load(address string) (any, error) {
	return nil, fmt.Errorf("%w: %s", errOutside, address)
}

// NewEngine compiles the shared schemas of one spec, which other schemas
// name as "#/$defs/<name>". It is a spec.NewEngine. A broken shared
// schema is a problem, even when no route uses it.
func NewEngine(defs map[string]spec.Schema) (spec.Engine, error) {
	e := &Engine{compiler: newCompiler(), inline: newCompiler(), inlined: map[string]*jsonschema.Schema{}}
	doc := make(map[string]any, len(defs))
	for name, s := range defs {
		doc[name] = s
	}
	// AddResource fails only for an address that is not absolute or was
	// added before. A new compiler and the fixed defsURL meet neither.
	_ = e.compiler.AddResource(defsURL, map[string]any{"$defs": doc})
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		if _, err := e.compile(defsURL + "#" + (&url.URL{Fragment: "/$defs/" + escapeToken(name)}).EscapedFragment()); err != nil {
			problems = append(problems, fmt.Errorf("the schema %s cannot be used: %w", name, err))
		}
	}
	return e, errors.Join(problems...)
}

// escapeToken writes a name as one step of a JSON pointer.
func escapeToken(name string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
}

// newCompiler makes a JSON Schema compiler set up the way the proxy
// checks: draft 2020-12, formats and JSON text checked, nothing fetched.
func newCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(outside{})
	c.AssertFormat()
	// A string holding JSON, marked with contentMediaType and
	// contentSchema, is read and checked too (Peter, 2026-10-30).
	c.RegisterVocabulary(jsonText())
	c.AssertVocabs()
	for _, f := range ignoredFormats() {
		c.RegisterFormat(&jsonschema.Format{Name: f, Validate: func(any) error { return nil }})
	}
	c.RegisterFormat(&jsonschema.Format{Name: "int32", Validate: bits(32)})
	c.RegisterFormat(&jsonschema.Format{Name: "int64", Validate: bits(64)})
	return c
}

// ignoredFormats are the formats the JSON Schema library knows but the
// proxy does not check. Only date-time, date, email, uuid, uri, ipv4, ipv6,
// int32 and int64 are checked as rules (Peter, 2026-10-29).
func ignoredFormats() []string {
	return []string{
		"json-pointer", "relative-json-pointer", "duration", "period", "hostname", "time",
		"iri", "uri-reference", "iri-reference", "uri-template", "semver", "regex",
		"idn-email", "idn-hostname",
	}
}

// bits checks that a whole number fits in a signed integer of that size.
func bits(size uint) func(any) error {
	limit := new(big.Int).Lsh(big.NewInt(1), size-1)
	low := new(big.Int).Neg(limit)
	return func(v any) error {
		n, ok := new(big.Int).SetString(fmt.Sprint(v), 10)
		if !ok {
			return nil
		}
		if n.Cmp(low) < 0 || n.Cmp(limit) >= 0 {
			return fmt.Errorf("%w in %d bits", errBits, size)
		}
		return nil
	}
}

// schema compiles one schema of a route, the cheap way when it can, and
// names it as what in a problem. The compiler copies all it has read each
// time it meets a new place, so a check per call made loading grow with
// the calls times the shapes; the SSM spec took about 950 MB of
// short-lived memory (Peter, 2026-11-01). So a schema that is only a $ref
// uses the check of the shared schema it names, built once, and a schema
// that needs nothing else is compiled apart from the shared ones, once for
// each text.
func (e *Engine) schema(s spec.Schema, what string) (*jsonschema.Schema, error) {
	if s == nil {
		return nil, nil //nolint:nilnil // no schema, so nothing to check
	}
	compiled, err := e.compileSchema(s)
	if err != nil {
		return nil, fmt.Errorf("the schema of %s cannot be used: %w", what, err)
	}
	return compiled, nil
}

func (e *Engine) compileSchema(s spec.Schema) (*jsonschema.Schema, error) {
	if target, ok := onlyRef(s); ok {
		return e.compile(defsURL + target)
	}
	if !standsAlone(s) {
		// It names shared schemas: given an address of its own, with
		// each $ref pointing into the shared ones.
		e.next++
		address := fmt.Sprintf("%s/route/%d.json", base, e.next)
		if err := e.compiler.AddResource(address, toShared(s)); err != nil {
			return nil, err
		}
		return e.compile(address)
	}
	// Printed with Go's syntax, maps in key order, so the same schema
	// always gives the same text.
	text := fmt.Sprintf("%#v", s)
	if compiled, ok := e.inlined[text]; ok {
		return compiled, nil
	}
	e.next++
	address := fmt.Sprintf("%s/inline/%d.json", base, e.next)
	if err := e.inline.AddResource(address, s); err != nil {
		return nil, err
	}
	compiled, err := e.inline.Compile(address)
	if err != nil {
		return nil, err
	}
	e.inlined[text] = compiled
	return compiled, nil
}

// onlyRef is the $ref of v, when v holds nothing but a $ref within the
// shared schemas.
func onlyRef(v map[string]any) (string, bool) {
	if len(v) != 1 {
		return "", false
	}
	ref, ok := v["$ref"].(string)
	return ref, ok && strings.HasPrefix(ref, "#")
}

// toShared copies JSON data, pointing each $ref within the document into
// the shared schemas' document.
func toShared(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for key, child := range x {
			if ref, isText := child.(string); key == "$ref" && isText && strings.HasPrefix(ref, "#") {
				child = defsURL + ref
			}
			out[key] = toShared(child)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, child := range x {
			out[i] = toShared(child)
		}
		return out
	}
	return v
}

// standsAlone tells whether a schema needs nothing else: it has no
// reference to elsewhere and names no place others could point to.
func standsAlone(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			switch key {
			case "$ref", "$dynamicRef", "$id", "$anchor", "$dynamicAnchor", "$schema", "$vocabulary":
				return false
			}
			if !standsAlone(value) {
				return false
			}
		}
	case []any:
		for _, value := range x {
			if !standsAlone(value) {
				return false
			}
		}
	}
	return true
}

func (e *Engine) compile(address string) (*jsonschema.Schema, error) {
	return e.compiler.Compile(address)
}

// insideJSON is the location token between a string that holds JSON and a
// place inside that JSON. It can't be a JSON property name the proxy sees,
// since the JSON reader never gives a name holding a zero byte from text
// that doesn't write one.
const insideJSON = "\x00json"

// jsonText checks a string marked contentMediaType: application/json with
// a contentSchema, such as an SQS message in MessageBody: the string must
// be JSON, and that JSON must match the contentSchema. The JSON Schema
// library can do this itself, but reports the wrong place for the string,
// so the proxy does it here. With contentEncoding: base64 beside them, as
// for a Kinesis record's Data, the string is base64 text and is decoded
// first.
func jsonText() *jsonschema.Vocabulary {
	return &jsonschema.Vocabulary{
		URL: "https://spec.hotline.invalid/vocab/json-text",
		Compile: func(ctx *jsonschema.CompilerContext, obj map[string]any) (jsonschema.SchemaExt, error) {
			mediaType, _ := obj["contentMediaType"].(string)
			if _, ok := obj["contentSchema"]; !ok || !strings.EqualFold(mediaType, "application/json") {
				return nil, nil //nolint:nilnil // no such keywords here, as the library asks
			}
			encoding, _ := obj["contentEncoding"].(string)
			return &jsonTextSchema{
				inner:  ctx.Enqueue([]string{"contentSchema"}),
				base64: strings.EqualFold(encoding, "base64"),
			}, nil
		},
	}
}

type jsonTextSchema struct {
	inner  *jsonschema.Schema
	base64 bool
}

func (s *jsonTextSchema) Validate(ctx *jsonschema.ValidatorContext, v any) {
	text, isText := v.(string)
	if isText && s.base64 {
		raw, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			ctx.AddError(&kind.ContentEncoding{Want: "base64"})
			return
		}
		text = string(raw)
	}
	decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if !isText || err != nil {
		ctx.AddError(&kind.ContentMediaType{Want: "application/json"})
		return
	}
	if err := ctx.Validate(s.inner, decoded, []string{insideJSON}); err != nil {
		ctx.AddErr(err)
	}
}
