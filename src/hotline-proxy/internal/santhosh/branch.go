package santhosh

import (
	"maps"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"hotline-proxy/internal/spec"
)

// branchNote is what spec.BranchNote says on one schema with oneOf or
// anyOf: which branch each value of a property means.
type branchNote struct {
	property string
	values   map[string]int
	// defaultBranch is the branch for a missing property or a value with
	// no branch of its own, -1 when there is none.
	defaultBranch int
	// branches is how many branches the choice has, and anyOf says the
	// choice is an anyOf, not a oneOf.
	branches int
	anyOf    bool
}

// branchNotes are the notes of one spec's schemas by the address of the
// schema that holds them. They are written while the spec is loaded and
// only read after, so many calls may read them at once.
type branchNotes map[string]*branchNote

// branchVocabulary reads spec.BranchNote. It never adds an error: a note
// changes only which problems a failed choice reports, never pass or
// fail, as OpenAPI says of a discriminator.
func branchVocabulary(notes branchNotes) *jsonschema.Vocabulary {
	return &jsonschema.Vocabulary{
		URL: "https://spec.hotline.invalid/vocab/branch",
		Compile: func(ctx *jsonschema.CompilerContext, obj map[string]any) (jsonschema.SchemaExt, error) {
			raw, ok := obj[spec.BranchNote].(map[string]any)
			if !ok {
				return nil, nil //nolint:nilnil // no such keyword here, as the library asks
			}
			keyword, _ := raw["choice"].(string)
			choice, _ := obj[keyword].([]any)
			property, _ := raw["property"].(string)
			note := &branchNote{property: property, values: map[string]int{}, defaultBranch: -1, branches: len(choice), anyOf: keyword == "anyOf"}
			values, _ := raw["values"].(map[string]any)
			// spec writes each position as a Go int.
			for value, i := range values {
				note.values[value], _ = i.(int)
			}
			if i, has := raw["default"].(int); has {
				note.defaultBranch = i
			}
			notes[ctx.Enqueue(nil).Location] = note
			// The note checks nothing, so it needs no compiled form.
			return nil, nil //nolint:nilnil // nothing to check, as the library allows
		},
	}
}

// noBranchPicked is a failed choice whose property is missing or holds a
// value with no branch, while its note gives no default. location is where
// the property is, and values are the values the note knows.
type noBranchPicked struct {
	location []string
	values   []string
}

// pickedBranch is the error of the branch a failed choice's note picks.
// With no branch to pick, noBranch says where the property is. Both are
// nil when the choice has no note, or when not every branch failed, as
// when two branches pass a oneOf: the choice's own error stands then.
// When every branch failed, the library lists their errors in the
// branches' order.
func (notes branchNotes) pickedBranch(e *jsonschema.ValidationError, value any) (branch *jsonschema.ValidationError, noBranch *noBranchPicked) {
	note := notes[e.SchemaURL]
	_, isAnyOf := e.ErrorKind.(*kind.AnyOf)
	if note == nil || note.anyOf != isAnyOf || len(e.Causes) != note.branches {
		return nil, nil
	}
	object, _ := value.(map[string]any)
	field, _ := object[note.property].(string)
	i, found := note.values[field]
	if !found {
		i = note.defaultBranch
	}
	if i < 0 {
		return nil, &noBranchPicked{
			location: append(slices.Clone(e.InstanceLocation), note.property),
			values:   slices.Sorted(maps.Keys(note.values)),
		}
	}
	return e.Causes[i], nil
}
