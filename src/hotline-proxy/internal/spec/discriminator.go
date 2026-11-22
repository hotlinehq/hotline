package spec

import (
	"maps"
	"slices"
	"strings"
)

// BranchNote is the key under which spec hands the engine what an OpenAPI
// discriminator says, on a schema with oneOf or anyOf (Peter, 2026-11-22,
// M3). Its value is an object:
//
//	{"property": "type", "values": {"text": 0, "image": 1}, "default": 1}
//
// values maps each value of the property to the position of its branch in
// the oneOf or anyOf, counted from 0, and default, when there is one, is
// the branch for a missing property or a value with no branch of its own.
// Each position is a Go int. choice says which list the positions count
// in: "oneOf" or "anyOf". The note never
// changes pass or fail, as OpenAPI says of a discriminator: it only says
// which branch's problems to report when the choice fails.
const BranchNote = "x-hotline-branch"

// noteBranches writes a BranchNote beside every discriminator in s that
// sits next to a oneOf or anyOf, and reports a discriminator written
// wrongly. where names the schema in a problem. A discriminator of
// OpenAPI's allOf form, or with no choice beside it, checks nothing, as
// OpenAPI says, and gets no note.
func (b *builder) noteBranches(v any, where string) {
	switch x := v.(type) {
	case map[string]any:
		// Only spec writes the note: one the spec's author wrote is dropped.
		delete(x, BranchNote)
		if d, ok := x["discriminator"].(map[string]any); ok {
			if keyword, choice, ok := choiceOf(x); ok {
				if note, ok := b.branchNote(d, keyword, choice, where); ok {
					x[BranchNote] = note
				}
			}
		}
		for _, key := range slices.Sorted(maps.Keys(x)) {
			b.noteBranches(x[key], where)
		}
	case []any:
		for _, child := range x {
			b.noteBranches(child, where)
		}
	}
}

// choiceOf is the oneOf or anyOf list of a schema.
func choiceOf(s map[string]any) (string, []any, bool) {
	if list, ok := s["oneOf"].([]any); ok {
		return "oneOf", list, true
	}
	list, ok := s["anyOf"].([]any)
	return "anyOf", list, ok
}

// branchNote reads one discriminator. Its mapping, defaultMapping and
// the branches' own names (OpenAPI's implicit mapping) all name shared
// schemas, which each branch must name in its $ref.
func (b *builder) branchNote(d map[string]any, keyword string, choice []any, where string) (map[string]any, bool) {
	property, _ := d["propertyName"].(string)
	if property == "" {
		b.problem("%s: its discriminator has no propertyName", where)
		return nil, false
	}
	// The branches by the shared schema they name, as "#/$defs/<name>",
	// which is how the engine's copy writes them.
	branches := map[string]int{}
	for i, branch := range choice {
		if m, ok := branch.(map[string]any); ok {
			if ref, ok := m["$ref"].(string); ok {
				if name, shared := strings.CutPrefix(ref, defsPrefix); shared {
					branches[name] = i
				}
			}
		}
	}
	values := map[string]any{}
	mapped := map[int]bool{}
	mapping, _ := d["mapping"].(map[string]any)
	ok := true
	for _, value := range slices.Sorted(maps.Keys(mapping)) {
		target, _ := mapping[value].(string)
		i, found := b.branchIndex(target, branches, where, "mapping")
		if found {
			values[value] = i
			mapped[i] = true
		}
		ok = ok && found
	}
	// A branch the mapping leaves out is meant by its schema's name
	// (OpenAPI's implicit mapping). A mapped branch is meant only by its
	// mapped values, so a problem lists the values the spec gives.
	for name, i := range branches {
		if _, taken := values[name]; !taken && !mapped[i] {
			values[name] = i
		}
	}
	note := map[string]any{"property": property, "values": values, "choice": keyword}
	if target, has := d["defaultMapping"].(string); has {
		i, found := b.branchIndex(target, branches, where, "defaultMapping")
		note["default"] = i
		ok = ok && found
	}
	return note, ok
}

// branchIndex finds the position of the branch a mapping value names: a
// schema name, such as ImageBlock or Pet.v1, or a $ref to one, such as
// #/components/schemas/ImageBlock. A name is looked up first, as OpenAPI
// recommends, so a name with a dot is still a name.
func (b *builder) branchIndex(target string, branches map[string]int, where, field string) (int, bool) {
	name := target
	if _, isName := b.sharedSchemas()[target]; !isName {
		var isRef bool
		name, isRef = strings.CutPrefix(target, sharedPrefix)
		if !isRef && strings.ContainsAny(target, "#/.") {
			b.problem("%s: its discriminator's %s names %s, which is not in this spec", where, field, target)
			return 0, false
		}
	}
	if _, exists := b.sharedSchemas()[name]; !exists {
		b.problem("%s: its discriminator's %s names %s, which the spec does not have", where, field, name)
		return 0, false
	}
	i, isBranch := branches[name]
	if !isBranch {
		b.problem("%s: its discriminator's %s names %s, which is not one of its branches", where, field, name)
	}
	return i, isBranch
}

// sharedSchemas are the spec's shared schemas by name.
func (b *builder) sharedSchemas() map[string]any {
	all, _ := b.at("/components/schemas")
	m, _ := all.(map[string]any)
	return m
}
