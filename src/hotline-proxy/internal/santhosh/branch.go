package santhosh

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

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
				return nil, nil
			}
			note, err := readBranchNote(raw, obj)
			if err != nil {
				return nil, err
			}
			notes[ctx.Enqueue(nil).Location] = note
			// The note checks nothing, so it needs no compiled form.
			return nil, nil
		},
	}
}

// errBranchNote marks a branch note the engine cannot read. spec writes
// every note, so this is a mistake in spec, and the spec is refused rather
// than every value pointed at the first branch.
var errBranchNote = errors.New("the branch note Hotline wrote cannot be read, a fault in Hotline")

// readBranchNote reads the note spec wrote on a schema, obj, checking
// every part of it.
func readBranchNote(raw, obj map[string]any) (*branchNote, error) {
	property, _ := raw["property"].(string)
	if property == "" {
		return nil, fmt.Errorf("%w: it names no property", errBranchNote)
	}
	keyword, _ := raw["choice"].(string)
	choice, isChoice := obj[keyword].([]any)
	if keyword != "oneOf" && keyword != "anyOf" || !isChoice {
		return nil, fmt.Errorf("%w: it names no oneOf or anyOf beside it", errBranchNote)
	}
	values, isObject := raw["values"].(map[string]any)
	if !isObject {
		return nil, fmt.Errorf("%w: its values are not an object", errBranchNote)
	}
	note := &branchNote{property: property, values: map[string]int{}, defaultBranch: -1, branches: len(choice), anyOf: keyword == "anyOf"}
	for _, value := range slices.Sorted(maps.Keys(values)) {
		i, err := position(values[value], len(choice), value)
		if err != nil {
			return nil, err
		}
		note.values[value] = i
	}
	if v, has := raw["default"]; has {
		i, err := position(v, len(choice), "the default")
		if err != nil {
			return nil, err
		}
		note.defaultBranch = i
	}
	return note, nil
}

// position reads a branch's position, a json.Number from 0 to one less
// than the number of branches. what names it in a problem.
func position(v any, branches int, what string) (int, error) {
	n, _ := v.(json.Number)
	i, err := strconv.Atoi(string(n))
	if err != nil {
		return 0, fmt.Errorf("%w: the position of %s is not a whole number", errBranchNote, what)
	}
	if i < 0 || i >= branches {
		return 0, fmt.Errorf("%w: the position of %s is not a whole number from 0 to %d", errBranchNote, what, branches-1)
	}
	return i, nil
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
