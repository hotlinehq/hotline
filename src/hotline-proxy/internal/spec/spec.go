// Package spec reads our copy of a vendor's OpenAPI spec: its route
// templates, its x-hotline-redact marks and the secrets named in its
// security section. It uses them to redact the vendor address and to pick
// the headers a record may hold.
package spec

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
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
	// ("path", "query", "header" and "response") and then by name. Header
	// names are lower case.
	marks map[string]map[string]mark
	// checks are what a call on this route is checked against.
	checks *checks
}

type document struct {
	OpenAPI      string              `yaml:"openapi"`
	ActionHeader string              `yaml:"x-hotline-action-header"` //nolint:tagliatelle // the name of our OpenAPI extension
	ActionField  string              `yaml:"x-hotline-action-field"`  //nolint:tagliatelle // the name of our OpenAPI extension
	Servers      []server            `yaml:"servers"`
	Paths        map[string]pathItem `yaml:"paths"`
}

// server is one entry of a servers list. Only the path of its address is
// used, as the config names the hosts.
type server struct {
	URL string `yaml:"url"`
}

// pathItem lists a path's methods. Everything else about a route, its
// parameters, responses and x-hotline-redact marks included, is read by the
// builder, which follows $ref.
type pathItem struct {
	// Servers, when given, replace the spec's for this path.
	Servers []server   `yaml:"servers"`
	Get     *operation `yaml:"get"`
	Put     *operation `yaml:"put"`
	Post    *operation `yaml:"post"`
	Delete  *operation `yaml:"delete"`
	Options *operation `yaml:"options"`
	Head    *operation `yaml:"head"`
	Patch   *operation `yaml:"patch"`
	Trace   *operation `yaml:"trace"`
	// Query is OpenAPI 3.2's QUERY method: it only reads, like GET, and
	// its call carries a body saying what to look up.
	Query *operation `yaml:"query"`
	// AdditionalOperations holds any other method, keyed by its name
	// written exactly as it is sent (OpenAPI 3.2).
	AdditionalOperations map[string]*operation `yaml:"additionalOperations"`
}

// operation holds what Parse reads of an operation itself: its servers,
// which replace its path's when given.
type operation struct {
	Servers []server `yaml:"servers"`
}

// operationServers are the method's own servers, nil when it has none.
func (p pathItem) operationServers(method string) []server {
	op := p.fixedOperations()[method]
	if op == nil {
		op = p.AdditionalOperations[method]
	}
	if op == nil {
		return nil
	}
	return op.Servers
}

// Parse reads and checks a spec. name only labels the problems it reports.
// newEngine makes the engine that checks values against the spec's JSON
// Schemas. Every problem is listed, not only the first one.
func Parse(name string, data []byte, newEngine NewEngine) (*Spec, error) {
	var doc document
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s is not YAML or JSON: %w", ErrInvalid, name, err)
	}
	// Only OpenAPI 3.2 is read (Peter, 2026-11-21). A 3.1 file moves over
	// by changing its version line alone, so the message says how. The
	// patch number never changes what a spec may say, so any 3.2.x loads.
	if !strings.HasPrefix(doc.OpenAPI, "3.2.") {
		found := "has no openapi line"
		if doc.OpenAPI != "" {
			found = "says openapi " + doc.OpenAPI
		}
		return nil, fmt.Errorf("%w: %s %s, but Hotline reads OpenAPI 3.2 only: change the line to openapi: 3.2.0", ErrInvalid, name, found)
	}
	b := newBuilder(name, data, newEngine)
	s := &Spec{actionHeader: doc.ActionHeader, actionField: doc.ActionField, secretHeaders: map[string]bool{}, secretQuery: map[string]bool{}}
	var problems []error
	// A template two methods share is read for each, but its problems are
	// told once.
	told := map[string]bool{}
	for key, item := range doc.Paths {
		pathBases := basePaths(item.Servers, doc.Servers)
		problems = append(problems, item.methodProblems(name, pathBases[0]+key)...)
		for method, operationKeys := range item.operationKeys() {
			for _, base := range basePaths(item.operationServers(method), item.Servers, doc.Servers) {
				template := base + key
				t, err := s.readTemplate(name, template)
				if err != nil && !told[template] {
					problems = append(problems, err)
				}
				told[template] = true
				r := route{
					method:   method,
					template: template,
					action:   t.action,
					words:    t.words,
					rest:     strings.HasSuffix(t.segments[len(t.segments)-1], "+}"),
					segments: t.segments,
				}
				where := fmt.Sprintf("%s %s %s", name, method, template)
				r.checks = b.checks(key, operationKeys, where)
				r.checks.request = b.requestCheck(r.checks, t.segments, where)
				r.marks = r.checks.marks
				s.routes = append(s.routes, r)
			}
		}
	}
	// What the security section says apps send to log in is secret. The
	// Authorization header that other ways use is never recorded anyway.
	for _, cred := range b.schemes {
		switch cred.in {
		case "header":
			s.secretHeaders[strings.ToLower(cred.name)] = true
		case "query":
			s.secretQuery[cred.name] = true
		}
	}
	problems = append(problems, b.problems...)
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return s, nil
}

// template is a route template read into its parts.
type template struct {
	action   string
	words    []queryWord
	segments []string
}

// readTemplate reads a route template: the action or query words it may
// name after #, and the pieces of its path.
func (s *Spec) readTemplate(name, route string) (template, error) {
	path, action, words, err := s.splitAction(name, route)
	problems := []error{err}
	segments := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for i, segment := range segments {
		if strings.HasSuffix(segment, "+}") && i != len(segments)-1 {
			problems = append(problems, fmt.Errorf("%w: %s: the route %s has %s before its end, but only the last part of a path may take the rest of it",
				ErrInvalid, name, route, segment))
		}
	}
	return template{action: action, words: words, segments: segments}, errors.Join(problems...)
}

// basePaths are the base paths of the first list of servers that has any,
// each once, in the order listed: an operation's own servers win over its
// path's, and a path's over the spec's, as OpenAPI says. With no servers
// at all, the base path is empty.
func basePaths(lists ...[]server) []string {
	for _, servers := range lists {
		var bases []string
		for _, sv := range servers {
			if base := basePath(sv.URL); !slices.Contains(bases, base) {
				bases = append(bases, base)
			}
		}
		if len(bases) > 0 {
			return bases
		}
	}
	return []string{""}
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

// operationKeys maps each method the path lists to where its operation
// sits in the path item: its own field, such as get, or its key under
// additionalOperations.
func (p pathItem) operationKeys() map[string][]string {
	all := map[string][]string{}
	for method, op := range p.fixedOperations() {
		if op != nil {
			all[method] = []string{strings.ToLower(method)}
		}
	}
	for method, op := range p.AdditionalOperations {
		if op != nil && additionalMethodProblem(method) == "" {
			all[method] = []string{"additionalOperations", method}
		}
	}
	return all
}

// fixedOperations are the methods with a field of their own in a path item.
func (p pathItem) fixedOperations() map[string]*operation {
	return map[string]*operation{
		http.MethodGet: p.Get, http.MethodPut: p.Put, http.MethodPost: p.Post,
		http.MethodDelete: p.Delete, http.MethodOptions: p.Options, http.MethodHead: p.Head,
		http.MethodPatch: p.Patch, http.MethodTrace: p.Trace, "QUERY": p.Query,
	}
}

// hasOwnField says whether a method has a field of its own in a path
// item, in any case.
func (p pathItem) hasOwnField(method string) bool {
	_, has := p.fixedOperations()[strings.ToUpper(method)]
	return has
}

// additionalMethodProblem says what is wrong with a key of
// additionalOperations, empty when nothing is. OpenAPI 3.2 forbids a
// method with a field of its own there, in any case, and a key must be a
// method name at all.
func additionalMethodProblem(method string) string {
	switch {
	case pathItem{}.hasOwnField(method):
		return fmt.Sprintf("lists %s under additionalOperations, but that method has its own field, %s: write it there", method, strings.ToLower(method))
	case !isMethodName(method):
		return fmt.Sprintf("lists %q under additionalOperations, which is not a method name", method)
	}
	return ""
}

// methodProblems are the additionalOperations keys a spec may not have,
// in the order of the keys.
func (p pathItem) methodProblems(name, template string) []error {
	var problems []error
	for _, method := range slices.Sorted(maps.Keys(p.AdditionalOperations)) {
		if problem := additionalMethodProblem(method); problem != "" {
			problems = append(problems, fmt.Errorf("%w: %s: the path %s %s", ErrInvalid, name, template, problem))
		}
	}
	return problems
}

// isMethodName says whether text can be an HTTP method: one or more
// letters, digits or the few marks HTTP allows in a name (RFC 9110,
// section 5.6.2), so no space, colon or slash.
func isMethodName(text string) bool {
	if text == "" {
		return false
	}
	for _, c := range text {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		if !letter && !digit && !strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			return false
		}
	}
	return true
}

// basePath is the path of a servers address, such as /v1 in
// https://api.x.com/v1, which goes in front of every route. The scheme and
// host are not used, as the config names the hosts, so a variable in them,
// such as {region}, is skipped rather than read. A variable in the path
// stays in the route, like any path value.
func basePath(address string) string {
	if _, rest, absolute := strings.Cut(address, "://"); absolute {
		_, path, _ := strings.Cut(rest, "/")
		address = "/" + path
	}
	return strings.TrimSuffix(address, "/")
}
