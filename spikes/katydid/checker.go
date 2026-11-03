// Package katydidcheck is a spike: check JSON and XML bodies against one JSON Schema with katydid.
package katydidcheck

import (
	"sync"

	"go.katydid.org.za/validator-go-jsonschema/jsonschema"
)

// Checker holds one loaded schema. A loaded katydid matcher is not safe to share
// between goroutines (measured: wrong answers), so every check takes a lock.
// For real parallel use, create one Checker per worker.
type Checker struct {
	mu   sync.Mutex
	m    jsonschema.Matcher
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
	root, defs := parseSchema(schemaJSON)
	return &Checker{m: m, root: root, defs: defs, sp: newXMLStreamingParser(&resolver{defs}, root)}, nil
}

// CheckJSON reports whether the JSON body is valid. No reason is given: katydid answers only yes or no.
func (c *Checker) CheckJSON(body []byte) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.MatchBytes(body)
}

// CheckXML reports whether the XML body is valid, reading it by the same schema.
// The error is set only when the body is not well-formed XML or has a DOCTYPE.
func (c *Checker) CheckXML(body []byte) (bool, error) {
	h, err := toHedge(body, c.root, c.defs)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m.MatchParser(newFixed(h))
}

// CheckXMLStream is CheckXML without any tree: the body is read piece by piece straight into the matcher.
func (c *Checker) CheckXMLStream(body []byte) (bool, error) {
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
