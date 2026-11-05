// Package santhosh checks values against the JSON Schemas of a spec with
// the santhosh-tekuri library, as spec.SchemaCompiler and spec.Schema. It
// is the only package that knows that library; config hands NewCompiler
// to spec.Parse.
package santhosh

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"hotline-proxy/internal/spec"
)

// specURL is the address the spec is known by inside the JSON Schema
// compiler. Nothing is ever fetched from it.
const specURL = "https://spec.hotline.invalid/openapi.json"

// errOutside marks a $ref to a document other than the spec.
var errOutside = errors.New("outside the spec")

// errBits marks a whole number too large for its format.
var errBits = errors.New("does not fit")

// Compiler compiles the schemas of one spec. $ref is followed only within
// the spec. It is used while a spec is loaded, by one goroutine.
type Compiler struct {
	compiler *jsonschema.Compiler
	// inline compiles schemas that need nothing else from the spec, such
	// as { type: string } on a header, apart from the spec, and inlined
	// keeps each one by its text, so the same schema is compiled once.
	inline  *jsonschema.Compiler
	inlined map[string]*jsonschema.Schema
	// inlineNext numbers the schemas given to inline.
	inlineNext int
}

// outside refuses every document but the spec itself, so a $ref to
// another file or address is a problem.
type outside struct{}

func (outside) Load(address string) (any, error) {
	return nil, fmt.Errorf("%w: %s", errOutside, address)
}

// NewCompiler takes the spec, read as plain JSON data with its numbers as
// json.Number. It is a spec.NewSchemaCompiler, and never fails.
func NewCompiler(doc any) (spec.SchemaCompiler, error) {
	c := &Compiler{compiler: newCompiler(), inline: newCompiler(), inlined: map[string]*jsonschema.Schema{}}
	// AddResource fails only for an address that is not absolute or was
	// added before. A new compiler and the fixed specURL meet neither.
	_ = c.compiler.AddResource(specURL, doc)
	return c, nil
}

// Compile compiles the schema v found in the spec at pointer.
func (c *Compiler) Compile(v any, pointer string) (spec.Schema, error) {
	s, err := c.compileSchema(v, pointer)
	if err != nil {
		return nil, err
	}
	return &Schema{s: s}, nil
}

// CompileAt compiles the schema at pointer, such as a shared one under
// /components/schemas, so a broken one is found even when nothing uses it.
func (c *Compiler) CompileAt(pointer string) error {
	_, err := c.compile(pointer)
	return err
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

// compileSchema compiles the schema v found at pointer, the cheap way
// when it can. The compiler copies all it has read each time it meets a
// new place in the spec, so a check per call made loading grow with the
// calls times the shapes; the SSM spec took about 950 MB of short-lived
// memory (Peter, 2026-11-01). So a schema that is only a $ref within the
// spec uses the check of the shape it names, built once, and a schema
// that needs nothing else from the spec is compiled apart from it, once
// for each text.
func (c *Compiler) compileSchema(v any, pointer string) (*jsonschema.Schema, error) {
	if target, ok := onlyRef(v); ok {
		return c.compile(target)
	}
	if !standsAlone(v) {
		return c.compile(pointer)
	}
	// Printed with Go's syntax, maps in key order, so the same schema
	// always gives the same text.
	text := fmt.Sprintf("%#v", v)
	if compiled, ok := c.inlined[text]; ok {
		return compiled, nil
	}
	c.inlineNext++
	address := fmt.Sprintf("%s/inline/%d.json", strings.TrimSuffix(specURL, "/openapi.json"), c.inlineNext)
	var compiled *jsonschema.Schema
	err := c.inline.AddResource(address, v)
	if err == nil {
		compiled, err = c.inline.Compile(address)
	}
	if err != nil {
		// Built again in place, so the problem names where it is in the
		// spec. Only a broken spec gets here.
		return c.compile(pointer)
	}
	c.inlined[text] = compiled
	return compiled, nil
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

// standsAlone tells whether a schema needs nothing else from the spec: it
// has no reference to elsewhere and names no place others could point to.
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

func (c *Compiler) compile(pointer string) (*jsonschema.Schema, error) {
	return c.compiler.Compile(specURL + "#" + (&url.URL{Fragment: pointer}).EscapedFragment())
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
