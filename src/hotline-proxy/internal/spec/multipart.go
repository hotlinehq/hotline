package spec

import (
	"mime"
	"net/url"
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

// formRules is what a multipart/form-data content says about its file
// parts, by property name.
type formRules struct {
	files map[string]fileRule
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

// MultipartFiles says whether a request on the route, sent as contentType,
// is read part by part as it passes: a multipart/form-data body whose spec
// content is multipart/form-data. isFile says, by a part's name, whether
// the spec calls it a file, whose content is counted and never kept.
func (s *Spec) MultipartFiles(found Route, contentType string) (isFile func(name string) bool, ok bool) {
	rules := found.formRules(contentType)
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
	rules := found.formRules(contentType)
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

// formRules are the rules of the route's multipart/form-data content, for
// a request sent as contentType; nil when it is not read part by part.
func (found Route) formRules(contentType string) *formRules {
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

// formRules reads which properties of a multipart/form-data content's
// schema are files: a property, or a list's items, with format binary or
// a contentMediaType. Each file's types come from the content's encoding.
func (b *builder) formRules(media map[string]any, pointer string) *formRules {
	rules := &formRules{files: map[string]fileRule{}}
	schema, at := b.object(media["schema"], pointer+"/schema", b.name)
	props, _ := schema["properties"].(map[string]any)
	encoding, _ := media["encoding"].(map[string]any)
	for name, v := range props {
		raw, _ := v.(map[string]any)
		resolved, resolvedAt := b.object(raw, at+"/properties"+token(name), b.name)
		if !binary(resolved) {
			items, _ := b.object(resolved["items"], resolvedAt+"/items", b.name)
			if !binary(items) {
				continue
			}
		}
		rule := fileRule{showName: raw["x-hotline-redact"] == false || resolved["x-hotline-redact"] == false}
		entry, _ := encoding[name].(map[string]any)
		if listed := str(entry, "contentType"); listed != "" {
			for t := range strings.SplitSeq(listed, ",") {
				rule.types = append(rule.types, strings.TrimSpace(t))
			}
		}
		rules.files[name] = rule
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
func (f *formRules) fileFindings(files []FormPart) []Finding {
	var findings []Finding
	for _, p := range files {
		property := propertyOf(p.Name)
		if types := f.files[property].types; !typeListed(types, p.ContentType) {
			findings = append(findings, Finding{Part: partBody, Place: whole + "." + property, Rule: "content_type", Expected: strings.Join(types, ", ")})
		}
	}
	return findings
}

// textForm puts the text parts together as a form, for the check: the
// parts read as text, and, as unread, the names of the parts that came but
// were not read, file parts and text parts not kept or sent as JSON. It
// returns why a text part was not read, if one was not.
func textForm(parts FormParts) (form []byte, unread []string, skipped string) {
	values := url.Values{}
	for _, p := range parts.Files {
		unread = append(unread, propertyOf(p.Name))
	}
	for _, p := range parts.Text {
		switch {
		case p.TooLarge:
			skipped = "body_too_large"
		case sentAsJSON(p.ContentType):
			// JSON parts are not read yet (Peter, 2026-11-24, Q6).
			skipped = "unsupported_content_type"
		default:
			values.Add(p.Name, string(p.Value))
			continue
		}
		unread = append(unread, propertyOf(p.Name))
	}
	return []byte(values.Encode()), unread, skipped
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

func sentAsJSON(contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return isJSON(mediaType)
}
