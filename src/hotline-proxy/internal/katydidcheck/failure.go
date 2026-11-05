package katydidcheck

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Failure is a ValidationError the way the proxy reports it (see internal/spec/check.go):
// the place like $.Contents[1].Size, the broken rule by its JSON Schema keyword, and what the schema expects.
// For a missing field the place ends with the name of the missing field.
type Failure struct { //nolint:errname // a finding about a body, not an error value
	Place    string
	Rule     string
	Expected string
	// Where is the failure as katydid reported it.
	Where *ValidationError
}

func (f *Failure) Error() string {
	s := "invalid at " + f.Place
	if f.Rule != "" {
		s += ": " + f.Rule
		if f.Expected != "" {
			s += " " + f.Expected
		}
	}
	return s
}

func (f *Failure) Unwrap() error { return f.Where }

// RuleNotAllowed is the rule of a field or value that the schema has no place for,
// such as a field next to additionalProperties false.
const RuleNotAllowed = "notAllowed"

// formats are the names katydid gives the formats, by the JSON Schema name where they differ.
var formats = map[string]string{ //nolint:gochecknoglobals // a fixed list
	"uuid": "uuid", "email": "email", "datetime": "date-time", "date": "date", "time": "time", "uri": "uri",
	"ipv4": "ipv4", "ipv6": "ipv6", "int32": "int32", "int64": "int64", "hostname": "hostname", "duration": "duration",
}

// types are katydid's tests of a type, by the JSON Schema type.
var types = map[string]string{ //nolint:gochecknoglobals // a fixed list
	"stringType": "string", "integerZeroFraction": "integer", "integer": "integer", "number": "number",
	"boolType": "boolean", "nullType": "null",
}

// describe turns katydid's failure into the proxy's terms. Katydid names the rule as its grammar writes it,
// for example maximum(double(5)), missing eq($string,"name") or at most 3 items; this only renames it.
func describe(ve *ValidationError) *Failure {
	f := &Failure{Place: placeOf(ve.Path), Where: ve}
	rule := firstRule(ve.Rule)
	switch {
	case rule == "" || rule == "not allowed":
		f.Rule = RuleNotAllowed
	case strings.HasPrefix(rule, "missing "):
		f.Rule = "required"
		if name, ok := quoted(firstPart(strings.TrimPrefix(rule, "missing "), " and ")); ok {
			f.Place += "." + name
		}
	case strings.HasPrefix(rule, "at least "), strings.HasPrefix(rule, "at most "):
		f.Rule, f.Expected = countRule(rule)
		if strings.HasPrefix(rule, "at most ") && len(ve.Path) > 0 {
			// the place is the list or object that has too many
			f.Place = placeOf(ve.Path[:len(ve.Path)-1])
		}
	default:
		f.Rule, f.Expected = funcRule(rule, ve.Value)
	}
	return f
}

// firstRule is the first of the rules katydid joins by " or ". A test of the tag, which says the value
// could also have been an object or a list, is taken only when nothing else is named.
func firstRule(rule string) string {
	parts := splitTop(rule, " or ")
	for _, part := range parts {
		if !strings.HasPrefix(part, "eq($tag,") {
			return part
		}
	}
	return parts[0]
}

// countRule reads "at least 2 items" as minItems 2, and "at most 1 fields" as maxProperties 1.
func countRule(rule string) (string, string) {
	words := strings.Fields(rule)
	if len(words) != 4 {
		return rule, ""
	}
	name := map[string]string{"least items": "minItems", "most items": "maxItems", "least fields": "minProperties", "most fields": "maxProperties"}[words[1]+" "+words[3]]
	return name, words[2]
}

// funcRule reads a test such as maximum(double(5)) as the keyword and what it expects.
func funcRule(rule, value string) (string, string) {
	name, args, _ := strings.Cut(rule, "(")
	args = strings.TrimSuffix(args, ")")
	if t, ok := types[name]; ok {
		return "type", t
	}
	if f, ok := formats[name]; ok {
		return "format", f
	}
	switch name {
	case "eq":
		kind, v, _ := strings.Cut(args, ",")
		if kind == "$tag" {
			t, _ := quoted(v)
			return "type", t
		}
		return "const", unquoted(v)
	case "or":
		var values []string
		for _, part := range splitTop(args, ",") {
			_, v, _ := strings.Cut(strings.TrimPrefix(part, "eq("), ",")
			values = append(values, unquoted(strings.TrimSuffix(v, ")")))
		}
		return "enum", strings.Join(values, ", ")
	case "enum":
		inner := args[strings.Index(args, "{")+1 : strings.LastIndex(args, "}")]
		var values []string
		for _, v := range splitTop(inner, ",") {
			values = append(values, unquoted(v))
		}
		return "enum", strings.Join(values, ", ")
	case "regex":
		return "pattern", unquoted(splitTop(args, ",")[0])
	case "xor":
		return "oneOf", ""
	case "minmaxLength":
		limits := splitTop(args, ",")
		if least, err := strconv.Atoi(limits[0]); err == nil && utf8.RuneCountInString(value) < least {
			return "minLength", limits[0]
		}
		return "maxLength", limits[len(limits)-1]
	}
	// maximum(double(5)), maxLength(2), multipleOf(double(2)) and the like name their keyword
	return strings.TrimSuffix(name, "Big"), strings.TrimSuffix(strings.TrimPrefix(args, "double("), ")")
}

// firstPart is the text before the first sep that is outside quotes and brackets.
func firstPart(s, sep string) string { return splitTop(s, sep)[0] }

// splitTop splits s at every sep that is outside quotes and brackets.
func splitTop(s, sep string) []string {
	var parts []string
	depth, inQuote, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inQuote && c == '\\':
			i++
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '(' || c == '{' || c == '[':
			depth++
		case c == ')' || c == '}' || c == ']':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(parts, s[start:])
}

// quoted is the text of the first quoted string in s, such as name in eq($string,"name").
func quoted(s string) (string, bool) {
	i := strings.Index(s, `"`)
	if i < 0 {
		return "", false
	}
	v, err := strconv.QuotedPrefix(s[i:])
	if err != nil {
		return "", false
	}
	text, err := strconv.Unquote(v)
	return text, err == nil
}

// unquoted is s without its quotes when it is a quoted string, and s itself otherwise.
func unquoted(s string) string {
	if text, ok := quoted(s); ok && strings.HasPrefix(strings.TrimSpace(s), `"`) {
		return text
	}
	return strings.TrimSpace(s)
}

func placeOf(path []any) string {
	var b strings.Builder
	b.WriteString("$")
	for _, step := range path {
		switch st := step.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(st) + "]")
		case string:
			b.WriteString("." + st)
		}
	}
	return b.String()
}
