package proxy_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cucumber/godog"
)

// fakeTerminal is what standard output is in a scenario: a terminal so
// many columns wide, or not a terminal at all.
type fakeTerminal struct {
	is    bool
	width int
}

func (t fakeTerminal) size() (int, bool) { return t.width, t.is }

// errClosed is standard output that can no longer be written to.
var errClosed = errors.New("standard output is closed")

type closedOutput struct{}

func (closedOutput) Write([]byte) (int, error) { return 0, errClosed }

// consoleOut is where the console exporter prints, or a closed output.
func (w *world) consoleOut() io.Writer {
	if w.consoleClosed {
		return closedOutput{}
	}
	return w.console
}

// ansi is a colour code the console writes around a word.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// colourCodes are the terminal's codes for the colour names the features
// use.
func colourCode(name string) string {
	return map[string]string{
		"red": "31", "green": "32", "yellow": "33", "magenta": "35", "cyan": "36", "dim": "2",
	}[name]
}

func consoleSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a console exporter$`, func() error {
		w.consoleExporter = "  - console: {}\n"
		return nil
	})
	sc.Step(`^the console exporter has color (\w+)$`, func(color string) error {
		w.consoleExporter = "  - console:\n      color: " + color + "\n"
		return nil
	})
	sc.Step(`^the console exporter is set with (.+)$`, func(setting string) error {
		switch setting {
		case "color loud":
			w.consoleExporter = "  - console:\n      color: loud\n"
		case "a second one":
			w.consoleExporter = "  - console: {}\n  - console: {}\n"
		case "a file path as well":
			w.consoleExporter = "  - console: {}\n    file:\n      path: ./calls\n"
		default:
			return godog.ErrPending
		}
		return nil
	})
	sc.Step(`^standard output is (not a terminal|a terminal (\d+) columns wide)(?:, with the environment variable COLUMNS (\d+))?$`,
		func(what, width, columns string) error {
			w.terminal = fakeTerminal{}
			if width != "" {
				n, _ := strconv.Atoi(width)
				w.terminal = fakeTerminal{is: true, width: n}
			}
			if columns != "" {
				w.setEnv("COLUMNS", columns)
			}
			return nil
		})
	sc.Step(`^the environment variable (\w+) is "([^"]*)"$`, func(name, value string) error {
		w.setEnv(name, value)
		return nil
	})
	sc.Step(`^standard output is closed$`, func() error {
		w.consoleClosed = true
		return nil
	})
	sc.Step(`^the shop vendor sends half of its reply, and the rest (\d+) milliseconds later$`, func(after int) error {
		w.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Length", "2048")
			_, _ = io.WriteString(rw, strings.Repeat("a", 1024))
			rw.(http.Flusher).Flush()
			time.Sleep(time.Duration(after) * time.Millisecond)
			_, _ = io.WriteString(rw, strings.Repeat("a", 1024))
		})
		return nil
	})
	sc.Step(`^the app sends GET /orders with a query (\d+) characters long$`, func(n int) error {
		return w.shopCall("GET", "/orders?"+strings.Repeat("q", n), true, "", "", "")
	})
	sc.Step(`^the app sends GET /orders with (the control character 9B|the byte FF, not UTF-8) in the query$`, func(what string) error {
		raw := map[string]string{"the control character 9B": "\u009b", "the byte FF, not UTF-8": "\xff"}[what]
		return w.shopCall("GET", "/orders?note=a"+raw+"b", true, "", "", "")
	})

	sc.Step(`^the console has (\d+) lines$`, func(n int) error {
		lines := w.consoleLines()
		if len(lines) != n {
			return failf("the console has %d lines, want %d: %q", len(lines), n, w.console.String())
		}
		return nil
	})
	sc.Step(`^the first line is the time and "([^"]*)"$`, func(want string) error {
		return w.consoleLine(0, `^\d\d:\d\d:\d\d\.\d\d\d `+regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the first line ends with "([^"]*)"$`, func(want string) error {
		return w.consoleLine(0, regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the second line is "([^"]*)", the time the call took and "([^"]*)"$`, func(start, end string) error {
		return w.consoleLine(1, `^ *`+regexp.QuoteMeta(start)+` \d+\.\d ms `+regexp.QuoteMeta(end)+`$`)
	})
	sc.Step(`^the second line starts with "([^"]*)"$`, func(want string) error {
		return w.consoleLine(1, `^ *`+regexp.QuoteMeta(want))
	})
	sc.Step(`^the second line ends with "([^"]*)"$`, func(want string) error {
		return w.consoleLine(1, ` `+regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the second line has the reply size (.+)$`, func(want string) error {
		return w.consoleLine(1, ` `+regexp.QuoteMeta(want)+` `)
	})
	sc.Step(`^the second line has no check status$`, func() error {
		lines := w.consoleLines()
		if len(lines) < 2 || regexp.MustCompile(` (valid|invalid|skipped)$`).MatchString(lines[1]) {
			return failf("the console is %q", w.console.String())
		}
		return nil
	})
	sc.Step(`^the second line's "<<" ends just before the method on the first line$`, func() error {
		lines := w.consoleLines()
		method := strings.Index(lines[0], ": ") + 2
		if strings.Index(lines[1], "<< ")+3 != method {
			return failf("the lines are not aligned:\n%s\n%s", lines[0], lines[1])
		}
		return nil
	})
	sc.Step(`^the third line is "([^"]*)", starting under the method$`, func(want string) error {
		lines := w.consoleLines()
		method := strings.Index(lines[0], ": ") + 2
		if len(lines) < 3 || lines[2] != strings.Repeat(" ", method)+want {
			return failf("the console is %q", w.console.String())
		}
		return nil
	})
	sc.Step(`^the address on the first line keeps its first (\d+) characters, then "…"$`, func(n int) error {
		lines := w.consoleLines()
		_, rest, _ := strings.Cut(lines[0], ": GET ")
		address := []rune(rest)
		if len(address) != n+1 || string(address[n:]) != "…" || !strings.HasPrefix("https://api.shop.test/orders?"+strings.Repeat("q", 200), string(address[:n])) {
			return failf("the address is %q, %d characters", rest, len(address))
		}
		return nil
	})
	sc.Step(`^the first line shows it as (.+)$`, func(shown string) error {
		return w.consoleLine(0, regexp.QuoteMeta("?note=a"+shown+"b")+`$`)
	})
	sc.Step(`^the console holds no character that could change the terminal$`, func() error {
		text := ansi.ReplaceAllString(w.console.String(), "")
		if !utf8.ValidString(text) {
			return failf("the console holds bytes that are not UTF-8: %q", text)
		}
		for _, r := range text {
			if unicode.IsControl(r) && r != '\n' {
				return failf("the console holds the character %U: %q", r, text)
			}
		}
		return nil
	})

	sc.Step(`^the console (has|has no) colour$`, func(has string) error {
		w.settle()
		coloured := strings.Contains(w.console.String(), "\x1b[")
		if coloured != (has == "has") || w.console.String() == "" {
			return failf("the console is %q", w.console.String())
		}
		return nil
	})
	sc.Step(`^the method (\S+) is bold (\w+)$`, func(method, colour string) error {
		return w.consoleHas("\x1b[1;" + colourCode(colour) + "m" + method + "\x1b[0m")
	})
	sc.Step(`^the status (\d+) is bold (\w+)$`, func(status, colour string) error {
		return w.consoleHas("\x1b[1;" + colourCode(colour) + "m" + status + " ")
	})
	sc.Step(`^the check status (\w+) is (\w+)$`, func(status, colour string) error {
		return w.consoleHas("\x1b[" + colourCode(colour) + "m" + status + "\x1b[0m")
	})
	sc.Step(`^the third line is dim, with "(\w+)" in red$`, func(side string) error {
		return w.consoleHas("\x1b[31m" + side + "\x1b[0m\x1b[2m: ")
	})
	sc.Step(`^"(\S+)" is red$`, func(word string) error {
		return w.consoleHas("\x1b[1;31m" + word + "\x1b[0m")
	})
	sc.Step(`^the program writes JSON$`, func() error {
		w.consoleJSON = true
		return nil
	})
	sc.Step(`^the first line is the record as JSON, with the method (\w+) and no colour$`, func(method string) error {
		lines := w.consoleLines()
		var record struct {
			Event      string         `json:"event"`
			Attributes map[string]any `json:"attributes"`
		}
		if strings.Contains(w.console.String(), "\x1b[") || json.Unmarshal([]byte(lines[0]), &record) != nil ||
			record.Event != "hotline.request" || record.Attributes["http.request.method"] != method {
			return failf("the console is %q", w.console.String())
		}
		return nil
	})
	sc.Step(`^the time the call took is yellow$`, func() error {
		w.settle()
		if !regexp.MustCompile(`\x1b\[33m\d+\.\d ms\x1b\[0m`).MatchString(w.console.String()) {
			return failf("the console is %q", w.console.String())
		}
		return nil
	})
}

// setEnv sets one variable of the proxy's environment.
func (w *world) setEnv(name, value string) {
	if w.env == nil {
		w.env = map[string]string{}
	}
	w.env[name] = value
}

// consoleLines are the console's lines without their colours, once the
// proxy has printed everything.
func (w *world) consoleLines() []string {
	w.settle()
	text := strings.TrimSuffix(ansi.ReplaceAllString(w.console.String(), ""), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// consoleLine checks one line, without its colours, against a pattern.
func (w *world) consoleLine(i int, pattern string) error {
	lines := w.consoleLines()
	if len(lines) <= i || !regexp.MustCompile(pattern).MatchString(lines[i]) {
		return failf("line %d of the console does not match %q: %q", i+1, pattern, w.console.String())
	}
	return nil
}

// consoleHas checks that the console printed text, colours and all.
func (w *world) consoleHas(text string) error {
	w.settle()
	if !strings.Contains(w.console.String(), text) {
		return failf("the console has no %q: %q", text, w.console.String())
	}
	return nil
}
