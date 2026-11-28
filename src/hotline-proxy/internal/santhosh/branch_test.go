package santhosh_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/santhosh"
	"hotline-proxy/internal/spec"
)

// A branch note is written by spec, never by a person, so a note the
// engine cannot read is a mistake in spec: it refuses the spec rather
// than pointing every value at the first branch.
var _ = Describe("Reading a branch note", func() {
	sut := branchNoteSUT{}

	It("reads a note whose positions are JSON numbers", func() {
		sut.forGoodNote()
		Expect(sut.BuildEngine()).To(Succeed())
	})

	DescribeTable("refuses a note it cannot read",
		func(change func(map[string]any), problem string) {
			sut.forGoodNote()
			change(sut.note)
			Expect(sut.BuildEngine()).To(MatchError(ContainSubstring(problem)))
		},
		Entry("no property", func(n map[string]any) { delete(n, "property") }, "names no property"),
		Entry("an unknown choice", func(n map[string]any) { n["choice"] = "allOf" }, "names no oneOf or anyOf"),
		Entry("a position that is not a JSON number", func(n map[string]any) {
			n["values"] = map[string]any{"text": 0}
		}, "position of text is not a whole number"),
		Entry("a position past the branches", func(n map[string]any) {
			n["values"] = map[string]any{"text": json.Number("2")}
		}, "position of text is not a whole number from 0 to 1"),
		Entry("a default that is not a JSON number", func(n map[string]any) { n["default"] = "first" }, "position of the default is not a whole number"),
		Entry("values that are not an object", func(n map[string]any) { n["values"] = "text" }, "values are not an object"),
	)
})

// branchNoteSUT is a block that is either text or an image, and the
// branch note on it that a test may change before the engine reads it.
type branchNoteSUT struct{ note map[string]any }

// forGoodNote is a note the engine reads: the type property picks the
// branch, text first and image second, and text when it is missing.
func (s *branchNoteSUT) forGoodNote() {
	s.note = map[string]any{
		"property": "type", "choice": "oneOf",
		"values":  map[string]any{"text": json.Number("0"), "image": json.Number("1")},
		"default": json.Number("0"),
	}
}

// BuildEngine builds an engine from the block and its note, and says why
// it could not.
func (s *branchNoteSUT) BuildEngine() error {
	_, err := santhosh.NewEngine(map[string]spec.Schema{
		"Text":  {"type": "object", "required": []any{"text"}},
		"Image": {"type": "object", "required": []any{"source"}},
		"Block": {
			"oneOf":         []any{map[string]any{"$ref": "#/$defs/Text"}, map[string]any{"$ref": "#/$defs/Image"}},
			spec.BranchNote: s.note,
		},
	})
	return err
}
