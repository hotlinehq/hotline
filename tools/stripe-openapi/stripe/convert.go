// Package stripe rewrites Stripe's own OpenAPI spec, written in OpenAPI 3.0,
// to the OpenAPI 3.2 the proxy reads (Peter, 2026-11-23, webhooks Q1).
//
// The spec is taken as Stripe wrote it. Only what 3.0 and 3.2 say
// differently is rewritten, and Stripe's own x- fields are dropped, so
// nothing in them changes what is checked (Q3): "may be null" is written
// as 3.2 writes it, and every list of allowed values is checked as given.
// Anything the tool does not know how to rewrite stops it, rather than
// being guessed.
package stripe

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	// ErrNotOpenAPI30 is returned for a file that is not an OpenAPI 3.0 spec.
	ErrNotOpenAPI30 = errors.New("not an OpenAPI 3.0 spec")
	// ErrNullableShape is returned for a nullable the tool cannot rewrite.
	ErrNullableShape = errors.New("cannot rewrite this nullable to 3.2")
	// ErrUnknownExtension is returned for an x- field that is not one of
	// Stripe's known ones, so a new one is looked at, not dropped unseen.
	ErrUnknownExtension = errors.New("an x- field the tool does not know")
	// ErrBooleanBound is returned for 3.0's true or false exclusiveMinimum
	// or exclusiveMaximum, which 3.2 writes as a number.
	ErrBooleanBound = errors.New("a true or false exclusive bound")
	// ErrUnknownPathValue is returned for a safe name no path value has.
	ErrUnknownPathValue = errors.New("no path value has this name")
)

// Options are the choices the tool takes beside the spec.
type Options struct {
	// SafePathValues names the path values to mark x-hotline-redact: false,
	// so records show them, such as webhook_endpoint for a we_ id. Every
	// other path value stays hidden.
	SafePathValues []string
	// SecretPathValues names the path values to mark x-hotline-redact:
	// true. Records hide every unmarked path value, but the file under
	// content: full hides only those marked true, so a path value that is
	// itself a secret, such as an ephemeral key, is named here.
	SecretPathValues []string
}

// stripeExtension says whether key is one of the x- fields Stripe's spec
// holds, all dropped: the spec is taken as written (Q3). Any other x-
// field stops the tool.
func stripeExtension(key string) bool {
	switch key {
	case "x-expandableFields", "x-expansionResources", "x-resourceId", "x-stripeBypassValidation", "x-stripeSpecFilename":
		return true
	}
	return false
}

// namesKeys says whether the map under key has names for keys, not
// keywords: a property, a schema or a form field may be called nullable
// or x-id.
func namesKeys(key string) bool {
	return key == "properties" || key == "schemas" || key == "encoding"
}

// Convert returns the 3.2 spec for Stripe's 3.0 spec in data.
func Convert(data []byte, options Options) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("read the spec: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, ErrNotOpenAPI30
	}
	root := doc.Content[0]
	version := valueOf(root, "openapi")
	if version == nil || !strings.HasPrefix(version.Value, "3.0.") {
		return nil, ErrNotOpenAPI30
	}
	version.Value = "3.2.0"
	if err := rewrite(root, false); err != nil {
		return nil, err
	}
	paths := valueOf(root, "paths")
	if err := markPathValues(paths, options.SafePathValues, "false"); err != nil {
		return nil, err
	}
	if err := markPathValues(paths, options.SecretPathValues, "true"); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	return out.Bytes(), encoder.Close()
}

// rewrite rewrites one node and everything inside it. keysAreNames is true
// for a map whose keys are names (see namesKeys), where a key called
// nullable or x-id is a name and not a keyword.
func rewrite(node *yaml.Node, keysAreNames bool) error {
	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if err := rewrite(item, false); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		kept := node.Content[:0]
		var nullable *yaml.Node
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if !keysAreNames {
				switch {
				case stripeExtension(key.Value):
					continue
				case strings.HasPrefix(key.Value, "x-"):
					return fmt.Errorf("%w: line %d: %s", ErrUnknownExtension, key.Line, key.Value)
				case key.Value == "nullable":
					nullable = value
					continue
				case (key.Value == "exclusiveMinimum" || key.Value == "exclusiveMaximum") && value.Tag == "!!bool":
					return fmt.Errorf("%w: line %d: %s", ErrBooleanBound, key.Line, key.Value)
				}
			}
			if err := rewrite(value, !keysAreNames && namesKeys(key.Value)); err != nil {
				return err
			}
			kept = append(kept, key, value)
		}
		node.Content = kept
		if nullable != nil {
			return mayBeNull(node, nullable)
		}
	}
	return nil
}

// mayBeNull writes a 3.0 nullable as 3.2 does. Stripe uses three shapes:
// a type, a type with a list of values, and an anyOf.
func mayBeNull(schema, nullable *yaml.Node) error {
	if nullable.Tag != "!!bool" {
		return fmt.Errorf("%w: line %d: nullable is %q, not true or false", ErrNullableShape, nullable.Line, nullable.Value)
	}
	if nullable.Value == "false" {
		return nil
	}
	for _, other := range []string{"$ref", "oneOf", "allOf"} {
		if valueOf(schema, other) != nil {
			return fmt.Errorf("%w: line %d: nullable beside %s", ErrNullableShape, nullable.Line, other)
		}
	}
	if choice := valueOf(schema, "anyOf"); choice != nil {
		choice.Content = append(choice.Content, &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{text("type"), text("null")}})
		return nil
	}
	kind := valueOf(schema, "type")
	if kind == nil || kind.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: line %d: nullable needs one type or an anyOf", ErrNullableShape, nullable.Line)
	}
	*kind = yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{text(kind.Value), text("null")}}
	if values := valueOf(schema, "enum"); values != nil {
		// Without null in the list, a null value would still fail it.
		values.Content = append(values.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"})
	}
	return nil
}

// markPathValues marks the named path values of every route
// x-hotline-redact: redact, true or false. A name no path value has stops
// the tool, so a typo shows.
func markPathValues(paths *yaml.Node, names []string, redact string) error {
	found := map[string]bool{}
	for _, route := range mapValues(paths) {
		for _, operation := range mapValues(route) {
			for _, parameter := range listItems(valueOf(operation, "parameters")) {
				in, name := valueOf(parameter, "in"), valueOf(parameter, "name")
				if in == nil || name == nil || in.Value != "path" || !slices.Contains(names, name.Value) {
					continue
				}
				parameter.Content = append(parameter.Content, text("x-hotline-redact"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: redact})
				found[name.Value] = true
			}
		}
	}
	for _, name := range names {
		if !found[name] {
			return fmt.Errorf("%w: %s", ErrUnknownPathValue, name)
		}
	}
	return nil
}

// valueOf is the value of key in a map, or nil.
func valueOf(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// mapValues are the values of a map, or none.
func mapValues(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	values := make([]*yaml.Node, 0, len(node.Content)/2)
	for i := 1; i < len(node.Content); i += 2 {
		values = append(values, node.Content[i])
	}
	return values
}

// listItems are the items of a list, or none.
func listItems(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}

// text is a plain text node.
func text(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
