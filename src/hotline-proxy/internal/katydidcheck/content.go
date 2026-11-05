package katydidcheck

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	kj "go.katydid.org.za/parser-go-json/json"
)

// Katydid does not read the JSON that a string holds. A string marked contentMediaType application/json, with a
// contentSchema, must hold JSON that matches the contentSchema. That is checked here, after katydid has checked the
// rest of the body, by walking the body along the schema. With contentEncoding base64 beside them, as for a Kinesis
// record's Data, the string is base64 text and is decoded first.

// inner keeps the checkers of the contentSchemas, made when first needed and shared after that.
type inner struct {
	mu     sync.Mutex
	pools  map[[32]byte]*Pool
	failed map[[32]byte]error
}

// hasContent tells whether the schema has a contentSchema anywhere, so other bodies are not walked.
func hasContent(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["contentSchema"]; ok {
			return true
		}
		for _, child := range x {
			if hasContent(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(x, hasContent)
	}
	return false
}

// contentJSON checks the JSON held by strings of the body. It returns nil when there is nothing wrong.
func (c *Checker) contentJSON(owner *Pool, body []byte) error {
	p := kj.NewJSONSchemaParser()
	p.Init(body)
	tree, err := buildTree(p)
	if err != nil {
		return err
	}
	r := &resolver{c.defs}
	failure, err := r.contentWalk(owner, c.root, tree, "$")
	if err != nil {
		return err
	}
	if failure != nil {
		return failure
	}
	return nil
}

func (r *resolver) contentWalk(owner *Pool, s schema, node any, place string) (*Failure, error) {
	s = r.deref(s)
	switch n := node.(type) {
	case string:
		return r.contentOf(owner, s, n, place)
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(n)) {
			if child := r.prop(s, []byte(key)); child != nil {
				if f, err := r.contentWalk(owner, child, n[key], place+"."+key); f != nil || err != nil {
					return f, err
				}
			}
		}
	case []any:
		for i, item := range n {
			if child := r.itemAt(s, i); child != nil {
				if f, err := r.contentWalk(owner, child, item, place+"["+strconv.Itoa(i)+"]"); f != nil || err != nil {
					return f, err
				}
			}
		}
	}
	return nil, nil
}

func (r *resolver) contentOf(owner *Pool, s schema, text, place string) (*Failure, error) {
	contentSchema, ok := s["contentSchema"].(map[string]any)
	mediaType, _ := s["contentMediaType"].(string)
	if !ok || !strings.EqualFold(mediaType, "application/json") {
		return nil, nil
	}
	fail := func(rule string) *Failure {
		return &Failure{Place: place, Rule: rule, Where: &ValidationError{Path: []any{}}}
	}
	if encoding, _ := s["contentEncoding"].(string); strings.EqualFold(encoding, "base64") {
		raw, err := base64.StdEncoding.DecodeString(text)
		if err != nil {
			return fail("base64"), nil
		}
		text = string(raw)
	}
	if !json.Valid([]byte(text)) {
		return fail("json"), nil
	}
	pool, err := owner.innerPool(contentSchema, r.defs)
	if err != nil {
		return nil, err
	}
	err = pool.WhyJSON([]byte(text))
	if err == nil {
		return nil, nil
	}
	var inside *Failure
	if !errors.As(err, &inside) {
		return fail("json"), nil
	}
	return &Failure{Place: place + strings.TrimPrefix(inside.Place, "$"), Rule: inside.Rule, Expected: inside.Expected, Where: inside.Where}, nil
}

// innerPool is the checker of a contentSchema, with the definitions of the schema it is in.
func (p *Pool) innerPool(contentSchema map[string]any, defs map[string]any) (*Pool, error) {
	doc := maps.Clone(contentSchema)
	if len(defs) > 0 {
		doc["$defs"] = defs
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(data)
	p.inner.mu.Lock()
	defer p.inner.mu.Unlock()
	if pool, ok := p.inner.pools[key]; ok {
		return pool, nil
	}
	if err, ok := p.inner.failed[key]; ok {
		return nil, err
	}
	pool, err := newPool(data, p.inner)
	if err != nil {
		p.inner.failed[key] = err
		return nil, err
	}
	p.inner.pools[key] = pool
	return pool, nil
}
