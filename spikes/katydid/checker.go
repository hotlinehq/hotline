// Package katydidcheck is a spike: check JSON and XML bodies against one JSON Schema with katydid.
package katydidcheck

import (
	"errors"
	"sync"

	"go.katydid.org.za/validator-go-jsonschema/jsonschema"
)

// ValidationError says where the first failure of a body is: the path of field names and
// list positions from the root (for example [Contents 1 Size]) and the failing value, if any.
type ValidationError = jsonschema.ValidationError

// Checker holds one loaded schema. A loaded katydid matcher is not safe to share
// between goroutines (measured: wrong answers), so every check takes a lock.
// For real parallel use, create one Checker per worker.
type Checker struct {
	mu   sync.Mutex
	m    jsonschema.Matcher
	why  jsonschema.Checker
	root schema
	defs map[string]any
	sp   *XMLStreamingParser
}

// Compile loads a JSON Schema. Local references must point at "#/definitions/...".
func Compile(schemaJSON []byte) (*Checker, error) {
	m, err := jsonschema.Compile(schemaJSON)
	if err != nil {
		return nil, err
	}
	// a second loaded copy that can say where a body first fails
	wm, err := jsonschema.NewMemoizer(schemaJSON)
	if err != nil {
		return nil, err
	}
	root, defs := parseSchema(schemaJSON)
	return &Checker{m: m, root: root, defs: defs, why: wm.(jsonschema.Checker), sp: newXMLStreamingParser(&resolver{defs}, root)}, nil
}

// CheckJSON reports whether the JSON body is valid. No reason is given: katydid answers only yes or no.
func (c *Checker) CheckJSON(body []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.MatchBytes(body)
}

// CheckXML reports whether the XML body is valid, reading it by the same schema. The body is read piece by piece
// straight into the matcher, with no tree. The error is set when the body is not well-formed XML or has a DOCTYPE.
func (c *Checker) CheckXML(body []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sp.Init(body)
	ok, err := c.m.MatchParser(c.sp)
	if err != nil {
		return false, err
	}
	if err := c.sp.finish(); err != nil {
		return false, err
	}
	return ok, nil
}

// WhyJSON checks the JSON body like CheckJSON, but says where the first failure is.
// It returns nil when the body is valid, a *ValidationError when it is not,
// and any other error when the body could not be read.
func (c *Checker) WhyJSON(body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.why.CheckBytes(body)
}

// WhyXML checks the XML body like CheckXML, but says where the first failure is.
func (c *Checker) WhyXML(body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sp.Init(body)
	err := c.why.CheckParser(c.sp)
	if err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			return err
		}
		// a body that is not well-formed XML counts as an error, even when it also fails the schema
		if ferr := c.sp.finish(); ferr != nil {
			return ferr
		}
		return err
	}
	return c.sp.finish()
}
