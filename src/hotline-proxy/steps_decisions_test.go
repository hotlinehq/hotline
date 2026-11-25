package proxy_test

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
	"go.yaml.in/yaml/v3"
)

// repoRoot is the repository's root, seen from this module, where the
// tests run.
const repoRoot = "../.."

// errNotFollowed is a Confirmation line that does not hold.
var errNotFollowed = errors.New("the decision is not followed")

// statRepo looks up a file by its path from the repository's root, never
// outside it.
func statRepo(path string) (os.FileInfo, error) {
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Stat(filepath.Clean(path))
}

// readRepo reads a file by its path from the repository's root, never
// outside it.
func readRepo(path string) ([]byte, error) {
	root, err := os.OpenRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(filepath.Clean(path))
}

// decisionSteps check the Confirmation lines of docs/decisions: rules
// about the proxy's source and the repository's other files
// (features/decisions.feature). They read files only.
func decisionSteps(sc *godog.ScenarioContext) {
	sc.Step(`^every file in docs/decisions ends with Tripwires, a Confirmation and a Revisit if line$`, everyDecisionHasAConfirmation)
	sc.Step(`^every file in docs/decisions has a scenario in this feature named after it$`, everyDecisionHasAScenario)
	sc.Step(`^the text "((?:[^"\\]|\\.)*)" is in proxy code only in (.+)$`, textOnlyIn)
	sc.Step(`^no proxy code imports "([^"]+)"$`, noProxyCodeImports)
	sc.Step(`^no proxy code holds "([^"]+)"$`, noProxyCodeHolds)
	sc.Step(`^the proxy code in (\S+) matches "([^"]+)"$`, proxyFileMatches)
	sc.Step(`^the proxy code in (\S+) holds "([^"]+)"$`, proxyFileHolds)
	sc.Step(`^the proxy code in (\S+) does not hold "([^"]+)"$`, proxyFileDoesNotHold)
	sc.Step(`^the workflow (\S+) starts only on a push to main or by hand$`, workflowStartsOnlyOnMainOrByHand)
	sc.Step(`^the workflow (\S+) has no concurrency group$`, workflowHasNoConcurrency)
	sc.Step(`^only the workflow (\S+) names the runner "([^"]+)"$`, onlyWorkflowNamesRunner)
	sc.Step(`^the file (\S+) matches "([^"]+)"$`, repoFileMatches)
	sc.Step(`^the file (\S+) holds "([^"]+)"$`, repoFileHolds)
	sc.Step(`^the Makefile's test target does not hold "([^"]+)"$`, makeTestDoesNotHold)
	sc.Step(`^every link in (\S+) leads to a file that exists$`, everyLinkExists)
	sc.Step(`^the proxy code in (\S+) does not import "([^"]+)"$`, proxyFileDoesNotImport)
	sc.Step(`^the linter (\S+) is on in \.golangci\.yaml$`, linterIsOn)
	sc.Step(`^every package with a fuzz test calls fuzzlog\.Capture\(\)$`, everyFuzzPackageCapturesLogs)
	sc.Step(`^the file (\S+) exists$`, repoFileExists)
	sc.Step(`^the package (\S+) declares no type named (\S+)$`, packageDeclaresNoType)
}

// decisionFiles are the decision files' names without .md, such as
// "aws-sqs".
func decisionFiles() ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, "docs/decisions/*.md"))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".md"))
	}
	return names, nil
}

func everyDecisionHasAConfirmation() error {
	names, err := decisionFiles()
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range names {
		text, err := readRepo(filepath.Join("docs/decisions", name+".md"))
		if err != nil {
			return err
		}
		if !tripwiresAtTheEnd(string(text)) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: decision files whose last section is not Tripwires, with a Confirmation and a Revisit if line: %s", errNotFollowed, strings.Join(missing, ", "))
	}
	return nil
}

// tripwiresAtTheEnd says whether a decision file's last section is its
// Tripwires, holding both a Confirmation line and a Revisit if line.
func tripwiresAtTheEnd(text string) bool {
	at := strings.LastIndex(text, "\n## ")
	if at < 0 {
		return false
	}
	last := text[at+1:]
	return strings.HasPrefix(last, "## Tripwires") &&
		strings.Contains(last, "- Confirmation:") && strings.Contains(last, "- Revisit if")
}

// scenarioName is a scenario's title in decisions.feature: the decision
// files it checks, then a full stop.
var scenarioName = regexp.MustCompile(`(?m)^\s*Scenario(?: Outline)?: ([a-z0-9 ,-]+?)\. `)

func everyDecisionHasAScenario() error {
	names, err := decisionFiles()
	if err != nil {
		return err
	}
	text, err := os.ReadFile("features/decisions.feature")
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, m := range scenarioName.FindAllSubmatch(text, -1) {
		for _, n := range strings.FieldsFunc(string(m[1]), func(r rune) bool { return r == ',' || r == ' ' }) {
			if n != "and" {
				named[n] = true
			}
		}
	}
	var missing []string
	for _, name := range names {
		if !named[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: decision files with no scenario in decisions.feature: %s", errNotFollowed, strings.Join(missing, ", "))
	}
	return nil
}

// proxyCode is every Go file of this module but its tests, by its path
// from the module, read without its comments.
func proxyCode() (map[string]string, error) {
	code := map[string]string{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == "testdata":
			return filepath.SkipDir
		case d.IsDir(), !strings.HasSuffix(path, ".go"), strings.HasSuffix(path, "_test.go"):
			return nil
		}
		text, err := withoutComments(path)
		code[filepath.ToSlash(path)] = text
		return err
	})
	if _, found := code["proxy.go"]; err == nil && !found {
		// Run from another folder, every "no proxy code" check would pass.
		return nil, fmt.Errorf("%w: found no proxy.go: the checks must run in the proxy's module folder", errNotFollowed)
	}
	return code, err
}

// withoutComments is a Go file printed again with its comments left out.
func withoutComments(path string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	err = format.Node(&out, fset, file)
	return out.String(), err
}

// fileList reads "a.go, b.go and c.go".
func fileList(text string) []string {
	text = strings.ReplaceAll(text, " and ", ",")
	var files []string
	for _, f := range strings.Split(text, ",") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files
}

func textOnlyIn(quoted, files string) error {
	text, err := strconv.Unquote(`"` + quoted + `"`)
	if err != nil {
		return err
	}
	code, err := proxyCode()
	if err != nil {
		return err
	}
	allowed := fileList(files)
	var found, elsewhere []string
	for path, src := range code {
		if !strings.Contains(src, text) {
			continue
		}
		found = append(found, path)
		if !slices.Contains(allowed, path) {
			elsewhere = append(elsewhere, path)
		}
	}
	slices.Sort(elsewhere)
	if len(elsewhere) > 0 {
		return fmt.Errorf("%w: %q is also in %s", errNotFollowed, text, strings.Join(elsewhere, ", "))
	}
	if len(found) == 0 {
		return fmt.Errorf("%w: %q is in no proxy code, so the check checks nothing: update the decision", errNotFollowed, text)
	}
	return nil
}

func noProxyCodeImports(path string) error {
	code, err := proxyCode()
	if err != nil {
		return err
	}
	var importers []string
	for file := range code {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			if name, _ := strconv.Unquote(spec.Path.Value); name == path || strings.HasPrefix(name, path+"/") {
				importers = append(importers, file)
			}
		}
	}
	slices.Sort(importers)
	if len(importers) > 0 {
		return fmt.Errorf("%w: %s imports %s", errNotFollowed, strings.Join(importers, ", "), path)
	}
	return nil
}

func noProxyCodeHolds(text string) error {
	code, err := proxyCode()
	if err != nil {
		return err
	}
	var holders []string
	for path, src := range code {
		if strings.Contains(src, text) {
			holders = append(holders, path)
		}
	}
	slices.Sort(holders)
	if len(holders) > 0 {
		return fmt.Errorf("%w: %q is in %s", errNotFollowed, text, strings.Join(holders, ", "))
	}
	return nil
}

func proxyFileMatches(path, pattern string) error {
	src, err := withoutComments(path)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	if !re.MatchString(src) {
		return fmt.Errorf("%w: %s has nothing matching %q", errNotFollowed, path, pattern)
	}
	return nil
}

func proxyFileHolds(path, text string) error {
	src, err := withoutComments(path)
	if err != nil {
		return err
	}
	if !strings.Contains(src, text) {
		return fmt.Errorf("%w: %s does not hold %q", errNotFollowed, path, text)
	}
	return nil
}

func proxyFileDoesNotHold(path, text string) error {
	src, err := withoutComments(path)
	if err != nil {
		return err
	}
	if strings.Contains(src, text) {
		return fmt.Errorf("%w: %s holds %q", errNotFollowed, path, text)
	}
	return nil
}

// workflow is the part of a GitHub workflow these checks read.
type workflow struct {
	On          map[string]yaml.Node `yaml:"on"`
	Concurrency *yaml.Node           `yaml:"concurrency"`
}

func readWorkflow(name string) (workflow, error) {
	var w workflow
	text, err := readRepo(filepath.Join(".github/workflows", name))
	if err != nil {
		return w, err
	}
	err = yaml.Unmarshal(text, &w)
	return w, err
}

func workflowStartsOnlyOnMainOrByHand(name string) error {
	w, err := readWorkflow(name)
	if err != nil {
		return err
	}
	for event, node := range w.On {
		switch event {
		case "workflow_dispatch":
		case "push":
			var push struct {
				Branches []string `yaml:"branches"`
				Tags     []string `yaml:"tags"`
			}
			if err := node.Decode(&push); err != nil {
				return err
			}
			if !slices.Equal(push.Branches, []string{"main"}) || len(push.Tags) > 0 {
				return fmt.Errorf("%w: %s starts on a push to %v, not only to main", errNotFollowed, name, append(push.Branches, push.Tags...))
			}
		default:
			return fmt.Errorf("%w: %s also starts on %s", errNotFollowed, name, event)
		}
	}
	if len(w.On) == 0 {
		return fmt.Errorf("%w: %s names no event it starts on", errNotFollowed, name)
	}
	return nil
}

func workflowHasNoConcurrency(name string) error {
	w, err := readWorkflow(name)
	if err != nil {
		return err
	}
	if w.Concurrency != nil {
		return fmt.Errorf("%w: %s has a concurrency group, which cancels a waiting run", errNotFollowed, name)
	}
	return nil
}

func onlyWorkflowNamesRunner(name, runner string) error {
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".github/workflows/*.y*ml"))
	if err != nil {
		return err
	}
	var users []string
	for _, p := range paths {
		text, err := readRepo(filepath.Join(".github/workflows", filepath.Base(p)))
		if err != nil {
			return err
		}
		if bytes.Contains(text, []byte(runner)) {
			users = append(users, filepath.Base(p))
		}
	}
	if !slices.Equal(users, []string{name}) {
		return fmt.Errorf("%w: the workflows naming %q are %v, not only %s", errNotFollowed, runner, users, name)
	}
	return nil
}

func repoFileMatches(path, pattern string) error {
	text, err := readRepo(path)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	if !re.Match(text) {
		return fmt.Errorf("%w: %s has nothing matching %q", errNotFollowed, path, pattern)
	}
	return nil
}

func repoFileHolds(path, text string) error {
	got, err := readRepo(path)
	if err != nil {
		return err
	}
	if !bytes.Contains(got, []byte(text)) {
		return fmt.Errorf("%w: %s does not hold %q", errNotFollowed, path, text)
	}
	return nil
}

// makeTestDoesNotHold reads the commands of the Makefile's test target:
// the tab-indented lines after "test:", up to the next line that is not.
func makeTestDoesNotHold(text string) error {
	makefile, err := readRepo("Makefile")
	if err != nil {
		return err
	}
	in, found := false, false
	for _, line := range strings.Split(string(makefile), "\n") {
		switch {
		case strings.HasPrefix(line, "test:"):
			in, found = true, true
		case in && strings.HasPrefix(line, "\t"):
			if command := strings.TrimSpace(line); !strings.HasPrefix(command, "#") && strings.Contains(command, text) {
				return fmt.Errorf("%w: make test runs %q", errNotFollowed, command)
			}
		case in:
			in = false
		}
	}
	if !found {
		return fmt.Errorf("%w: the Makefile has no test target", errNotFollowed)
	}
	return nil
}

// markdownLink is a link to a local file in Markdown, without an anchor.
var markdownLink = regexp.MustCompile(`\]\(([^)#:\s]+)(?:#[^)]*)?\)`)

func everyLinkExists(path string) error {
	text, err := readRepo(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	var broken []string
	for _, m := range markdownLink.FindAllSubmatch(text, -1) {
		if _, err := statRepo(filepath.Join(dir, string(m[1]))); err != nil {
			broken = append(broken, string(m[1]))
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("%w: %s links to files that do not exist: %s", errNotFollowed, path, strings.Join(broken, ", "))
	}
	return nil
}

func proxyFileDoesNotImport(path, pkg string) error {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return err
	}
	for _, spec := range f.Imports {
		if name, _ := strconv.Unquote(spec.Path.Value); name == pkg {
			return fmt.Errorf("%w: %s imports %s", errNotFollowed, path, pkg)
		}
	}
	return nil
}

// linterIsOn looks only in the list of linters that are on, not in their
// settings, which may name a linter that is off.
func linterIsOn(name string) error {
	text, err := readRepo(".golangci.yaml")
	if err != nil {
		return err
	}
	var lint struct {
		Linters struct {
			Enable []string `yaml:"enable"`
		} `yaml:"linters"`
	}
	if err := yaml.Unmarshal(text, &lint); err != nil {
		return err
	}
	if !slices.Contains(lint.Linters.Enable, name) {
		return fmt.Errorf("%w: the linter %s is not on in .golangci.yaml", errNotFollowed, name)
	}
	return nil
}

// everyFuzzPackageCapturesLogs reads every folder of this module whose
// tests hold a fuzz test, comments left out, and wants fuzzlog.Capture()
// called in one of its tests.
func everyFuzzPackageCapturesLogs() error {
	fuzz, capture := map[string]bool{}, map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == "testdata":
			return filepath.SkipDir
		case d.IsDir(), !strings.HasSuffix(path, "_test.go"):
			return nil
		}
		src, err := withoutComments(path)
		dir := filepath.ToSlash(filepath.Dir(path))
		fuzz[dir] = fuzz[dir] || strings.Contains(src, "\nfunc Fuzz")
		capture[dir] = capture[dir] || strings.Contains(src, "fuzzlog.Capture()")
		return err
	})
	if err != nil {
		return err
	}
	var missing []string
	for dir, has := range fuzz {
		if has && !capture[dir] {
			missing = append(missing, dir)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		return fmt.Errorf("%w: packages with fuzz tests that never call fuzzlog.Capture(): %s", errNotFollowed, strings.Join(missing, ", "))
	}
	if len(fuzz) == 0 {
		return fmt.Errorf("%w: found no fuzz tests, so the check checks nothing", errNotFollowed)
	}
	return nil
}

func repoFileExists(path string) error {
	_, err := statRepo(path)
	return err
}

func packageDeclaresNoType(dir, name string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if spec.(*ast.TypeSpec).Name.Name == name {
					return fmt.Errorf("%w: %s declares the type %s", errNotFollowed, p, name)
				}
			}
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("%w: %s holds no Go files", errNotFollowed, dir)
	}
	return nil
}
