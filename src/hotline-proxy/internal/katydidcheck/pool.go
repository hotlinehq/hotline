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
	schema     []byte
	copies     sync.Pool
	hasContent bool
	inner      *inner
}

// NewPool loads the schema once, so a schema that cannot be used is found now, not by a check.
func NewPool(schemaJSON []byte) (*Pool, error) {
	return newPool(schemaJSON, nil)
}

// newPool makes a pool. The pools of the contentSchemas of a schema share the cache of the pool they belong to,
// and do not load their own contentSchemas before a check needs them: they hold the same definitions, so that
// would never end.
func newPool(schemaJSON []byte, shared *inner) (*Pool, error) {
	first, err := newCopy(schemaJSON)
	if err != nil {
		return nil, err
	}
	p := &Pool{schema: schemaJSON, inner: shared}
	if shared == nil {
		p.inner = &inner{pools: map[[32]byte]*Pool{}, failed: map[[32]byte]error{}}
	}
	p.hasContent = hasContent(map[string]any(first.root))
	if p.hasContent && shared == nil {
		// a contentSchema that cannot be used is found now, not by a check
		if err := p.loadContent(first); err != nil {
			return nil, err
		}
	}
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
	err := c.WhyJSON(body)
	if err == nil && p.hasContent {
		// the JSON held by strings is checked after the rest of the body
		return c.contentJSON(p, body)
	}
	return err
}

// loadContent makes the checker of every contentSchema of the schema, so a broken one is found when the pool is made.
func (p *Pool) loadContent(c *Checker) error {
	var failure error
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if inner, ok := x["contentSchema"].(map[string]any); ok && failure == nil {
				_, failure = p.innerPool(inner, c.defs)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(map[string]any(c.root))
	for _, def := range c.defs {
		walk(def)
	}
	return failure
}

// WhyXML checks the XML body like WhyJSON, reading it by the schema.
func (p *Pool) WhyXML(body []byte) error {
	c := p.take()
	defer p.give(c)
	return c.WhyXML(body)
}
