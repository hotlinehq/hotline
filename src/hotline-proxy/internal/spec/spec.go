// Package spec reads our copy of a vendor's OpenAPI spec: its route
// templates, its x-hotline-redact marks and the secrets named in its
// security section. It uses them to redact the vendor address and to pick
// the headers a record may hold.
package spec

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrInvalid marks a spec the proxy cannot use.
var ErrInvalid = errors.New("spec: invalid")

// Spec is a read, checked vendor spec. It never changes after Parse.
type Spec struct {
	routes []route
	// actionHeader names the header that picks a route written as
	// /path#Header=value, such as X-Amz-Target. Empty when the spec has
	// no such routes.
	actionHeader string
	// actionField names the top-level body field that picks a route
	// written as /path#Field=value, such as Action for SNS and STS. Empty
	// when the spec has no such routes. A spec has at most one of the two.
	actionField   string
	secretHeaders map[string]bool
	secretQuery   map[string]bool
}

// mark is what an x-hotline-redact mark says about one value.
type mark int

const (
	unmarked mark = iota
	keep
	redact
)

type route struct {
	method   string
	template string
	segments []string
	// action is the value the action header or body field must have, for
	// a route written as /path#Name=value. Empty for an ordinary route.
	action string
	// words are the query words a call must have, for a route written as
	// /path#?word or /path#?word=value&other.
	words []queryWord
	// rest says the last segment, written {name+}, takes the rest of the
	// path.
	rest bool
	// marks hold the parameter marks by where the parameter lives
	// ("path", "query", "header" and "reply") and then by name. Header
	// names are lower case.
	marks map[string]map[string]mark
	// checks are what a call on this route is checked against.
	checks *checks
}

type document struct {
	OpenAPI      string `yaml:"openapi"`
	ActionHeader string `yaml:"x-hotline-action-header"` //nolint:tagliatelle // the name of our OpenAPI extension
	ActionField  string `yaml:"x-hotline-action-field"`  //nolint:tagliatelle // the name of our OpenAPI extension
	Servers      []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths      map[string]pathItem `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]securityScheme `yaml:"securitySchemes"`
	} `yaml:"components"`
}

type pathItem struct {
	Parameters []parameter `yaml:"parameters"`
	Get        *operation  `yaml:"get"`
	Put        *operation  `yaml:"put"`
	Post       *operation  `yaml:"post"`
	Delete     *operation  `yaml:"delete"`
	Options    *operation  `yaml:"options"`
	Head       *operation  `yaml:"head"`
	Patch      *operation  `yaml:"patch"`
	Trace      *operation  `yaml:"trace"`
}

type operation struct {
	Parameters []parameter         `yaml:"parameters"`
	Responses  map[string]response `yaml:"responses"`
}

type response struct {
	Headers map[string]struct {
		Redact any `yaml:"x-hotline-redact"` //nolint:tagliatelle // the name of our OpenAPI extension
	} `yaml:"headers"`
}

type parameter struct {
	Name   string `yaml:"name"`
	In     string `yaml:"in"`
	Redact any    `yaml:"x-hotline-redact"` //nolint:tagliatelle // the name of our OpenAPI extension
}

type securityScheme struct {
	Type string `yaml:"type"`
	In   string `yaml:"in"`
	Name string `yaml:"name"`
}

// Parse reads and checks a spec. name only labels the problems it reports.
// Every problem is listed, not only the first one.
func Parse(name string, data []byte) (*Spec, error) {
	var doc document
	err := yaml.Unmarshal(data, &doc)
	if err != nil || !strings.HasPrefix(doc.OpenAPI, "3.1.") {
		return nil, fmt.Errorf("%w: %s is not an OpenAPI 3.1 document", ErrInvalid, name)
	}
	b := newBuilder(name, data)
	s := &Spec{actionHeader: doc.ActionHeader, actionField: doc.ActionField, secretHeaders: map[string]bool{}, secretQuery: map[string]bool{}}
	var problems []error
	// A base path in the first servers address, such as /v1 in
	// https://api.x.com/v1, goes in front of every route.
	base := ""
	if len(doc.Servers) > 0 {
		if u, err := url.Parse(doc.Servers[0].URL); err == nil {
			base = strings.TrimSuffix(u.Path, "/")
		}
	}
	for key, item := range doc.Paths {
		template := base + key
		path, action, words, err := s.splitAction(name, template)
		if err != nil {
			problems = append(problems, err)
		}
		segments := strings.Split(strings.TrimSuffix(path, "/"), "/")
		for i, segment := range segments {
			if strings.HasSuffix(segment, "+}") && i != len(segments)-1 {
				problems = append(problems, fmt.Errorf("%w: %s: the route %s has %s before its end, but only the last part of a path may take the rest of it",
					ErrInvalid, name, template, segment))
			}
		}
		for method, op := range item.operations() {
			r := route{
				method:   method,
				template: template,
				action:   action,
				words:    words,
				rest:     strings.HasSuffix(segments[len(segments)-1], "+}"),
				segments: segments,
				marks:    map[string]map[string]mark{"path": {}, "query": {}, "header": {}, "reply": {}},
			}
			where := fmt.Sprintf("%s %s %s", name, method, template)
			r.checks = b.checks(key, strings.ToLower(method), where)
			r.checks.request = b.santhosh.request(s, r, where)
			// The method's own entries come last, so they win.
			for _, p := range append(append([]parameter{}, item.Parameters...), op.Parameters...) {
				problems = r.addMark(problems, where, p.In, p.Name, p.Redact)
			}
			for _, reply := range op.Responses {
				for header, h := range reply.Headers {
					problems = r.addMark(problems, where, "reply", header, h.Redact)
				}
			}
			s.routes = append(s.routes, r)
		}
	}
	for _, scheme := range doc.Components.SecuritySchemes {
		switch {
		case scheme.Type == "apiKey" && scheme.In == "header":
			s.secretHeaders[strings.ToLower(scheme.Name)] = true
		case scheme.Type == "apiKey" && scheme.In == "query":
			s.secretQuery[scheme.Name] = true
		}
	}
	problems = append(problems, b.problems...)
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return s, nil
}

// queryWord is a word a call's query must hold to take a route: only
// present, or with exactly the value given.
type queryWord struct {
	name     string
	value    string
	hasValue bool
}

// splitAction reads a route written as /path#Header=value, the way a
// vendor that names each action in a header is written (Peter,
// 2026-10-29), or as /path#Field=value, for a vendor that names it in a
// body field (Peter, 2026-11-01), or as /path#?word&other=value, for a
// vendor that names it with words in the query (Peter, 2026-10-31). The
// header or field must be the one the spec names in
// x-hotline-action-header or x-hotline-action-field, and a spec names only
// one of them.
func (s *Spec) splitAction(name, template string) (path, action string, words []queryWord, err error) {
	path, after, found := strings.Cut(template, "#")
	if !found {
		return template, "", nil, nil
	}
	if query, isWords := strings.CutPrefix(after, "?"); isWords {
		for _, pair := range strings.Split(query, "&") {
			word, value, hasValue := strings.Cut(pair, "=")
			if word == "" {
				return path, "", nil, fmt.Errorf("%w: %s: the route %s names an empty query word", ErrInvalid, name, template)
			}
			words = append(words, queryWord{name: word, value: value, hasValue: hasValue})
		}
		return path, "", words, nil
	}
	key, action, _ := strings.Cut(after, "=")
	switch {
	case action == "" || s.actionHeader == "" && s.actionField == "":
		return path, action, nil, fmt.Errorf("%w: %s: the route %s names %s, but the spec has neither x-hotline-action-header nor x-hotline-action-field",
			ErrInvalid, name, template, key)
	case s.actionHeader != "" && s.actionField != "":
		return path, action, nil, fmt.Errorf("%w: %s: the route %s can't be read: the spec names both x-hotline-action-header %q and x-hotline-action-field %q, and may name only one",
			ErrInvalid, name, template, s.actionHeader, s.actionField)
	case s.actionHeader != "" && !strings.EqualFold(key, s.actionHeader):
		return path, action, nil, fmt.Errorf("%w: %s: the route %s names the header %s, but the spec's x-hotline-action-header is %q",
			ErrInvalid, name, template, key, s.actionHeader)
	case s.actionField != "" && key != s.actionField:
		return path, action, nil, fmt.Errorf("%w: %s: the route %s names the field %s, but the spec's x-hotline-action-field is %q",
			ErrInvalid, name, template, key, s.actionField)
	}
	return path, action, nil, nil
}

func (p pathItem) operations() map[string]*operation {
	all := map[string]*operation{
		http.MethodGet: p.Get, http.MethodPut: p.Put, http.MethodPost: p.Post,
		http.MethodDelete: p.Delete, http.MethodOptions: p.Options, http.MethodHead: p.Head,
		http.MethodPatch: p.Patch, http.MethodTrace: p.Trace,
	}
	for method, op := range all {
		if op == nil {
			delete(all, method)
		}
	}
	return all
}

func (r route) addMark(problems []error, where, in, name string, value any) []error {
	var m mark
	switch value {
	case nil:
		return problems
	case true:
		m = redact
	case false:
		m = keep
	default:
		return append(problems, fmt.Errorf("%w: %s: x-hotline-redact on %s must be true or false", ErrInvalid, where, name))
	}
	if in != "path" && in != "query" {
		name = strings.ToLower(name)
	}
	// A cookie parameter has no entry: cookies are never recorded.
	if marks, ok := r.marks[in]; ok {
		marks[name] = m
	}
	return problems
}
