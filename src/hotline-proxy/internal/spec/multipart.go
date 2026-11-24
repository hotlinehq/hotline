package spec

import (
	"mime"
	"slices"
	"strings"
)

// FormParts is a multipart/form-data body as the proxy read it, part by
// part, as it passed: the text parts kept, the file parts only counted
// (Peter, 2026-11-24, M4).
type FormParts struct {
	// Text and Files are the text parts and the file parts, each in the
	// order they came.
	Text  []FormPart
	Files []FormPart
	// Broken says the body did not keep to the multipart framing: no
	// boundary, a part without a name, or a body cut off before its
	// closing boundary.
	Broken bool
}

// FormPart is one part of a multipart/form-data body.
type FormPart struct {
	// Name is the part's name in the form, such as model or
	// timestamp_granularities[].
	Name string
	// FileName is the file name the app gave, empty when it gave none.
	FileName    string
	ContentType string
	// Size is the bytes of the part's content.
	Size int64
	// Value is a text part's content; nil for a file part, and for a text
	// part not kept.
	Value []byte
	// TooLarge says a text part was not kept, as the text parts together
	// were over the limit.
	TooLarge bool
}

// RecordedFile is one file part of a call, as the record lists it.
type RecordedFile struct {
	Name string
	// FileName is REDACTED unless the spec marks the file
	// x-hotline-redact false.
	FileName    string
	ContentType string
	Size        int64
}

// partRules is what a multipart/form-data content says about its parts, by
// property name: which are files, and which are sent as JSON.
type partRules struct {
	files map[string]fileRule
	// jsonParts are the properties sent as one JSON part: their encoding
	// lists a JSON type, or, listing none, they are an object or a list of
	// objects, as OpenAPI 3.2 says (Peter, 2026-11-24, Q6).
	jsonParts map[string]bool
	// jsonLists are the JSON properties that are lists, each part one item,
	// so their parts are always read as a list, even when one came.
	jsonLists map[string]bool
}

// fileRule is what the spec says about one file part.
type fileRule struct {
	// types are the content types encoding lists for it, such as audio/*;
	// empty when it lists none, and any type passes (Peter, 2026-11-24,
	// Q2).
	types []string
	// showName says the spec marks the part x-hotline-redact false, so the
	// record shows its file name; hidden otherwise (Q4).
	showName bool
}

const multipartForm = "multipart/form-data"

// ReadAsFormParts says whether a request on the route, sent as contentType,
// is read part by part as it passes: a multipart/form-data body whose spec
// content is multipart/form-data. isFile says, by a part's name, whether
// the spec calls it a file, whose content is counted and never kept.
func (s *Spec) ReadAsFormParts(found Route, contentType string) (isFile func(name string) bool, ok bool) {
	rules := found.partRules(contentType)
	if rules == nil {
		return nil, false
	}
	return func(name string) bool {
		_, file := rules.files[propertyOf(name)]
		return file
	}, true
}

// RecordFiles lists the file parts of a request on the route, sent as
// contentType, as the record holds them: the file name REDACTED unless the
// spec marks the file x-hotline-redact false (Q4).
func (s *Spec) RecordFiles(found Route, contentType string, files []FormPart) []RecordedFile {
	rules := found.partRules(contentType)
	if rules == nil || len(files) == 0 {
		return nil
	}
	recorded := make([]RecordedFile, len(files))
	for i, f := range files {
		name := redacted
		if rules.files[propertyOf(f.Name)].showName {
			name = f.FileName
		}
		recorded[i] = RecordedFile{Name: f.Name, FileName: name, ContentType: f.ContentType, Size: f.Size}
	}
	return recorded
}

// partRules are the rules of the route's multipart/form-data content, for
// a request sent as contentType; nil when it is not read part by part.
func (found Route) partRules(contentType string) *partRules {
	if found.r.segments == nil || found.r.checks.body == nil {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != multipartForm {
		return nil
	}
	c, _ := found.r.checks.body.find(mediaType)
	if c == nil {
		return nil
	}
	return c.multipart
}

// propertyOf is the property a part's name belongs to: the name before any
// bracket, so timestamp_granularities[] and chunking_strategy[type] belong
// to timestamp_granularities and chunking_strategy, as in a form.
func propertyOf(name string) string {
	property, _, _ := strings.Cut(name, "[")
	return property
}

// partRules reads which properties of a multipart/form-data content's
// schema are files: a property, or a list's items, with format binary or a
// contentMediaType; each file's types come from the content's encoding. It
// also reads which are sent as JSON.
func (b *builder) partRules(media map[string]any, pointer string) *partRules {
	rules := &partRules{files: map[string]fileRule{}, jsonParts: map[string]bool{}, jsonLists: map[string]bool{}}
	schema, at := b.object(media["schema"], pointer+"/schema", b.name)
	props, _ := schema["properties"].(map[string]any)
	encoding, _ := media["encoding"].(map[string]any)
	for name, v := range props {
		raw, _ := v.(map[string]any)
		resolved, resolvedAt := b.object(raw, at+"/properties"+token(name), b.name)
		items, _ := b.object(resolved["items"], resolvedAt+"/items", b.name)
		entry, _ := encoding[name].(map[string]any)
		var listed []string
		if types := str(entry, "contentType"); types != "" {
			for t := range strings.SplitSeq(types, ",") {
				listed = append(listed, strings.TrimSpace(t))
			}
		}
		_, style := entry["style"]
		_, explode := entry["explode"]
		kind := kinds(raw, resolved)
		switch {
		case binary(resolved) || binary(items):
			rules.files[name] = fileRule{types: listed, showName: raw["x-hotline-redact"] == false || resolved["x-hotline-redact"] == false}
		case style || explode:
			// A style says the property is written as a form is, and
			// OpenAPI then ignores its contentType, as Stripe's spec has it.
		case listed != nil:
			rules.jsonParts[name] = slices.ContainsFunc(listed, func(t string) bool { return isJSON(strings.ToLower(t)) })
		default:
			rules.jsonParts[name] = slices.Contains(kind, "object") || slices.Contains(kind, "array") && slices.Contains(kinds(items, items), "object")
		}
		rules.jsonLists[name] = rules.jsonParts[name] && slices.Contains(kind, "array")
	}
	return rules
}

// binary says a schema describes a file: format binary, as specs written
// the 3.0 way say, or a contentMediaType, as 3.1 and 3.2 say.
func binary(schema map[string]any) bool {
	_, media := schema["contentMediaType"]
	return media || schema["format"] == "binary"
}

// fileFindings are the file parts whose type the spec does not list.
func (f *partRules) fileFindings(files []FormPart) []Finding {
	var findings []Finding
	for _, p := range files {
		property := propertyOf(p.Name)
		if types := f.files[property].types; !typeListed(types, p.ContentType) {
			findings = append(findings, Finding{Part: partBody, Place: whole + "." + property, Rule: "content_type", Expected: strings.Join(types, ", ")})
		}
	}
	return findings
}

// propertyParts are the text parts of one property, in the order they
// came.
type propertyParts struct {
	property string
	parts    []FormPart
}

// textByProperty groups the text parts by the property they belong to, in
// the order each property first came.
func textByProperty(parts FormParts) []propertyParts {
	var grouped []propertyParts
	at := map[string]int{}
	for _, p := range parts.Text {
		property := propertyOf(p.Name)
		i, ok := at[property]
		if !ok {
			i = len(grouped)
			at[property] = i
			grouped = append(grouped, propertyParts{property: property})
		}
		grouped[i].parts = append(grouped[i].parts, p)
	}
	return grouped
}

// jsonValue is the JSON value of a property sent as JSON: its one part, or
// a list of its parts, each one item, when the property is a list or came
// as several parts. ok is false
// when the property is not sent as JSON, or a part names a place inside it,
// as session[type] does: such parts are read as text.
func (f *partRules) jsonValue(group propertyParts) (value []byte, ok bool) {
	if !f.jsonParts[group.property] || slices.ContainsFunc(group.parts, func(p FormPart) bool { return p.Name != group.property }) {
		return nil, false
	}
	if len(group.parts) == 1 && !f.jsonLists[group.property] {
		return group.parts[0].Value, true
	}
	value = []byte{'['}
	for i, p := range group.parts {
		if i > 0 {
			value = append(value, ',')
		}
		value = append(value, p.Value...)
	}
	return append(value, ']'), true
}

// typeListed says whether a part's content type is one of types, which may
// end in /*; any type is when types is empty. A part that gives no type is
// text/plain, as RFC 7578 says.
func typeListed(types []string, contentType string) bool {
	if len(types) == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if contentType == "" {
		mediaType, err = "text/plain", nil
	}
	if err != nil {
		return false
	}
	major, _, _ := strings.Cut(mediaType, "/")
	return slices.ContainsFunc(types, func(t string) bool {
		return strings.EqualFold(t, mediaType) || strings.EqualFold(t, major+"/*") || t == "*/*"
	})
}
