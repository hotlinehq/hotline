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
	block := func(note map[string]any) map[string]spec.Schema {
		return map[string]spec.Schema{
			"Text":  {"type": "object", "required": []any{"text"}},
			"Image": {"type": "object", "required": []any{"source"}},
			"Block": {
				"oneOf":         []any{map[string]any{"$ref": "#/$defs/Text"}, map[string]any{"$ref": "#/$defs/Image"}},
				spec.BranchNote: note,
			},
		}
	}
	good := func() map[string]any {
		return map[string]any{
			"property": "type", "choice": "oneOf",
			"values":  map[string]any{"text": json.Number("0"), "image": json.Number("1")},
			"default": json.Number("0"),
		}
	}

	It("reads a note whose positions are JSON numbers", func() {
		_, err := santhosh.NewEngine(block(good()))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("refuses a note it cannot read",
		func(change func(map[string]any), problem string) {
			note := good()
			change(note)
			_, err := santhosh.NewEngine(block(note))
			Expect(err).To(MatchError(ContainSubstring(problem)))
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
