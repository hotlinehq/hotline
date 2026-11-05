package katydidcheck

import (
	"sync"

	"go.katydid.org.za/validator-go-jsonschema/jsonschema"
)

// Pool checks bodies against one JSON Schema, from many goroutines at the same time.
// A loaded katydid memoizer is not safe to share, so the pool keeps copies and each check takes one for its own use.
// Copies load in well under a millisecond, and a schema that loads once loads again.
// Definitions are read from "$defs" or "definitions", and "$ref" must point at one of them.
type Pool struct {
	schema []byte
	copies sync.Pool
}

// NewPool loads the schema once, so a schema that cannot be used is found now, not by a check.
func NewPool(schemaJSON []byte) (*Pool, error) {
	first, err := newCopy(schemaJSON)
	if err != nil {
		return nil, err
	}
	p := &Pool{schema: schemaJSON}
	p.copies.New = func() any {
		c, err := newCopy(p.schema)
		if err != nil {
			// the same schema loaded before
			panic(err)
		}
		return c
	}
	p.copies.Put(first)
	return p, nil
}

// newCopy is a Checker with a memoizer only: it checks, and it says where the first failure is.
func newCopy(schemaJSON []byte) (*Checker, error) {
	m, err := jsonschema.NewMemoizer(schemaJSON)
	if err != nil {
		return nil, err
	}
	root, defs := parseSchema(schemaJSON)
	return &Checker{m: m, root: root, defs: defs, why: m.(jsonschema.Checker), sp: newXMLStreamingParser(&resolver{defs}, root)}, nil
}

func (p *Pool) take() *Checker { return p.copies.Get().(*Checker) }

func (p *Pool) give(c *Checker) { p.copies.Put(c) }

// WhyJSON checks the JSON body. It returns nil when the body is valid, a *Failure when it is not,
// and any other error when the body could not be read.
func (p *Pool) WhyJSON(body []byte) error {
	c := p.take()
	defer p.give(c)
	return c.WhyJSON(body)
}

// WhyXML checks the XML body like WhyJSON, reading it by the schema.
func (p *Pool) WhyXML(body []byte) error {
	c := p.take()
	defer p.give(c)
	return c.WhyXML(body)
}
