package read

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
)

// LongestText is how many characters of a copied text a record holds.
const LongestText = 256

// At is the value at a path of property names in Go data, nil when there
// is none.
func At(v any, path []string) any {
	for _, step := range path {
		m, _ := v.(map[string]any)
		v = m[step]
	}
	return v
}

// Plain turns a value copied from a body into a record's value, when it is
// of one of the types: text cut to LongestText characters, true or false,
// or a json.Number as an int64, or else a float64. Text from an XML or
// form body is turned into the type its schema wants first, by the engine.
func Plain(v any, types []string) (any, bool) {
	switch x := v.(type) {
	case string:
		if !slices.Contains(types, "string") {
			return nil, false
		}
		return Cut(x), true
	case bool:
		return x, slices.Contains(types, "boolean")
	case json.Number:
		return number(string(x), types)
	}
	return nil, false
}

func number(text string, types []string) (any, bool) {
	if !slices.Contains(types, "integer") && !slices.Contains(types, "number") {
		return nil, false
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(text, 64)
	return f, err == nil && !math.IsInf(f, 0)
}

// Cut keeps the first LongestText characters of a text.
func Cut(text string) string {
	if runes := []rune(text); len(runes) > LongestText {
		return string(runes[:LongestText])
	}
	return text
}
