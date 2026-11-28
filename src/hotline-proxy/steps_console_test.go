package proxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cucumber/godog"

	proxy "hotline-proxy"
)

// fakeTerminal is what standard output is in a scenario: a terminal so
// many columns wide, or not a terminal at all.
type fakeTerminal struct {
	is    bool
	width int
	// slow is how long the terminal takes to say its width; asked counts
	// how often it was asked.
	slow  time.Duration
	asked *atomic.Int32
}

func (t fakeTerminal) size() (int, bool) {
	if t.asked != nil {
		t.asked.Add(1)
	}
	pause(t.slow)
	return t.width, t.is
}

// errClosed is standard output that can no longer be written to.
var errClosed = errors.New("standard output is closed")

type closedOutput struct{}

func (closedOutput) Write([]byte) (int, error) { return 0, errClosed }

// consoleOut is where the console exporter prints, or a closed output.
func (sut *consoleSUT) consoleOut() io.Writer {
	if sut.consoleClosed {
		return closedOutput{}
	}
	return sut.printed
}

// consoleOption is the console the test gives the proxy: none at all when
// the scenario starts the proxy the way a Go program that leaves it out does.
func (sut *consoleSUT) consoleOption() proxy.Console {
	if sut.noConsole {
		return proxy.Console{}
	}
	return proxy.Console{Out: sut.consoleOut(), Terminal: sut.terminal.size, JSON: sut.consoleJSON}
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

func consoleSteps(sc *godog.ScenarioContext, sut *consoleSUT) {
	sc.Step(`^a console exporter$`, func() error {
		sut.consoleExporter = "  - console: {}\n"
		return nil
	})
	sc.Step(`^the console exporter has color (\w+)$`, func(color string) error {
		sut.consoleExporter = "  - console:\n      color: " + color + "\n"
		return nil
	})
	sc.Step(`^the console exporter is written as "console:" with nothing after it$`, func() error {
		sut.consoleExporter = "  - console:\n"
		return nil
	})
	sc.Step(`^the console exporter is set with (.+)$`, func(setting string) error {
		switch setting {
		case "color loud":
			sut.consoleExporter = "  - console:\n      color: loud\n"
		case "a word instead of settings":
			sut.consoleExporter = "  - console: loud\n"
		case "colour always":
			sut.consoleExporter = "  - console:\n      colour: always\n"
		case "a second one":

			sut.consoleExporter = "  - console: {}\n  - console: {}\n"
		case "a file path as well":
			sut.consoleExporter = "  - console: {}\n    file:\n      path: ./calls\n"
		default:
			return godog.ErrPending
		}
		return nil
	})
	sc.Step(`^standard output is (not a terminal|a terminal (\d+) columns wide)(?:, with the environment variable COLUMNS (\d+))?$`,
		func(what, width, columns string) error {
			sut.terminal = fakeTerminal{}
			if width != "" {
				n, _ := strconv.Atoi(width)
				sut.terminal = fakeTerminal{is: true, width: n}
			}
			if columns != "" {
				sut.proxy.setEnv("COLUMNS", columns)
			}
			return nil
		})
	sc.Step(`^standard output is a terminal (\d+) columns wide, which takes (\d+) milliseconds to say so$`, func(width, slow int) error {
		sut.terminal = fakeTerminal{is: true, width: width, slow: time.Duration(slow) * time.Millisecond, asked: &atomic.Int32{}}
		return nil
	})
	sc.Step(`^the app sends GET /orders (\d+) times, one after another on one connection$`, func(n int) error {
		sut.vendors.vendor("api.shop.test")
		if err := sut.proxy.start(); err != nil {
			return err
		}
		proxyURL := &url.URL{Scheme: "http", Host: sut.proxy.addr, User: url.UserPassword("talon-shop-a", "x")}
		client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), MaxConnsPerHost: 1}}
		defer client.CloseIdleConnections()
		for range n {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://api.shop.test/orders", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer t0k3n")
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil
	})
	sc.Step(`^standard output was asked its width fewer than (\d+) times$`, func(n int) error {
		sut.proxy.settle()
		if asked := int(sut.terminal.asked.Load()); asked >= n {
			return failf("standard output was asked its width %d times", asked)
		}
		return nil
	})
	sc.Step(`^the environment variable (\w+) is "([^"]*)"$`, func(name, value string) error {
		sut.proxy.setEnv(name, value)
		return nil
	})
	sc.Step(`^the program starting the proxy gives no console$`, func() error {
		sut.noConsole = true
		return nil
	})
	sc.Step(`^standard output is closed$`, func() error {
		sut.consoleClosed = true
		return nil
	})
	sc.Step(`^the shop vendor sends half of its response, and the rest (\d+) milliseconds later$`, func(after int) error {
		sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Content-Length", "2048")
			_, _ = io.WriteString(rw, strings.Repeat("a", 1024))
			rw.(http.Flusher).Flush()
			pause(time.Duration(after) * time.Millisecond)
			_, _ = io.WriteString(rw, strings.Repeat("a", 1024))
		})
		return nil
	})
	sc.Step(`^the app sends GET /orders with a query (\d+) characters long$`, func(n int) error {
		return sut.app.shopCall("GET", "/orders?"+strings.Repeat("q", n), true, "", "", "")
	})
	sc.Step(`^a call to a host without an api ends, with (the control character 9B|the byte FF, not UTF-8|the right-to-left mark 202E) in the query$`, func(what string) error {
		raw := map[string]string{"the control character 9B": "\u009b", "the byte FF, not UTF-8": "\xff", "the right-to-left mark 202E": "\u202e"}[what]
		sut.app.target = "https://api.other.test/v1/things?note=a" + raw + "b"
		return sut.app.callWithBody(`{"thing":1}`, "Content-Type: application/json")
	})

	sc.Step(`^the vendor answers 200 with (the control character 9B|the right-to-left mark 202E|the language tag E0001) in the X-Request-Id header$`, func(what string) error {
		raw := map[string]string{"the control character 9B": "\u009b", "the right-to-left mark 202E": "\u202e", "the language tag E0001": "\U000e0001"}[what]
		sut.vendors.vendor("api.shop.test").setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("X-Request-Id", "a"+raw+"b")
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"orders":[]}`))
		})
		return nil
	})

	sc.Step(`^the console does not hold "([^"]*)"$`, func(text string) error {
		sut.proxy.settle()
		if strings.Contains(sut.printed.String(), text) {
			return failf("the console holds %q: %q", text, sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the console has (\d+) lines$`, func(n int) error {
		lines := sut.consoleLines()
		if len(lines) != n {
			return failf("the console has %d lines, want %d: %q", len(lines), n, sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the first line is the time and "([^"]*)"$`, func(want string) error {
		return sut.consoleLine(0, `^\d\d:\d\d:\d\d\.\d\d\d `+regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the first line ends with "([^"]*)"$`, func(want string) error {
		return sut.consoleLine(0, regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the second line is "([^"]*)", the time the call took and "([^"]*)"$`, func(start, end string) error {
		return sut.consoleLine(1, `^ *`+regexp.QuoteMeta(start)+` \d+\.\d ms `+regexp.QuoteMeta(end)+`$`)
	})
	sc.Step(`^the second line starts with "([^"]*)"$`, func(want string) error {
		return sut.consoleLine(1, `^ *`+regexp.QuoteMeta(want))
	})
	sc.Step(`^the second line ends with "([^"]*)"$`, func(want string) error {
		return sut.consoleLine(1, ` `+regexp.QuoteMeta(want)+`$`)
	})
	sc.Step(`^the second line has the response size (.+)$`, func(want string) error {
		return sut.consoleLine(1, ` `+regexp.QuoteMeta(want)+` `)
	})
	sc.Step(`^the second line has no check status$`, func() error {
		lines := sut.consoleLines()
		if len(lines) < 2 || regexp.MustCompile(` (valid|invalid|skipped)$`).MatchString(lines[1]) {
			return failf("the console is %q", sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the second line's "<<" ends just before the method on the first line$`, func() error {
		lines := sut.consoleLines()
		method := strings.Index(lines[0], ": ") + 2
		if strings.Index(lines[1], "<< ")+3 != method {
			return failf("the lines are not aligned:\n%s\n%s", lines[0], lines[1])
		}
		return nil
	})
	sc.Step(`^the third line is "([^"]*)", starting under the method$`, func(want string) error {
		lines := sut.consoleLines()
		method := strings.Index(lines[0], ": ") + 2
		if len(lines) < 3 || lines[2] != strings.Repeat(" ", method)+want {
			return failf("the console is %q", sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the address on the first line keeps its first (\d+) characters, then "…"$`, func(n int) error {
		lines := sut.consoleLines()
		_, rest, _ := strings.Cut(lines[0], ": GET ")
		address := []rune(rest)
		if len(address) != n+1 || string(address[n:]) != "…" || !strings.HasPrefix("https://api.shop.test/orders?"+strings.Repeat("q", 200), string(address[:n])) {
			return failf("the address is %q, %d characters", rest, len(address))
		}
		return nil
	})
	sc.Step(`^the first line shows it as (.+)$`, func(shown string) error {
		return sut.consoleLine(0, regexp.QuoteMeta("?note=a"+shown+"b")+`$`)
	})
	sc.Step(`^the console holds no character that could change the terminal$`, func() error {
		text := ansi.ReplaceAllString(sut.printed.String(), "")
		if !utf8.ValidString(text) {
			return failf("the console holds bytes that are not UTF-8: %q", text)
		}
		for _, r := range text {
			if (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)) && r != '\n' {
				return failf("the console holds the character %U: %q", r, text)
			}
		}
		return nil
	})

	sc.Step(`^the console (has|has no) colour$`, func(has string) error {
		sut.proxy.settle()
		coloured := strings.Contains(sut.printed.String(), "\x1b[")
		if coloured != (has == "has") || sut.printed.String() == "" {
			return failf("the console is %q", sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the method (\S+) is bold (\w+)$`, func(method, colour string) error {
		return sut.consoleHas("\x1b[1;" + colourCode(colour) + "m" + method + "\x1b[0m")
	})
	sc.Step(`^the status (\d+) is bold (\w+)$`, func(status, colour string) error {
		return sut.consoleHas("\x1b[1;" + colourCode(colour) + "m" + status + " ")
	})
	sc.Step(`^the status (\d+) is bold, with no colour$`, func(status string) error {
		return sut.consoleHas("\x1b[1m" + status + " ")
	})
	sc.Step(`^the check status (\w+) is (\w+)$`, func(status, colour string) error {
		return sut.consoleHas("\x1b[" + colourCode(colour) + "m" + status + "\x1b[0m")
	})
	sc.Step(`^the third line is dim, with "(\w+)" in red$`, func(side string) error {
		return sut.consoleHas("\x1b[31m" + side + "\x1b[0m\x1b[2m: ")
	})
	sc.Step(`^"(\S+)" is red$`, func(word string) error {
		return sut.consoleHas("\x1b[1;31m" + word + "\x1b[0m")
	})
	sc.Step(`^the program writes JSON$`, func() error {
		sut.consoleJSON = true
		return nil
	})
	sc.Step(`^the first line is the record as JSON, with the method (\w+) and no colour$`, func(method string) error {
		lines := sut.consoleLines()
		var record struct {
			Event      string         `json:"event"`
			Attributes map[string]any `json:"attributes"`
		}
		if strings.Contains(sut.printed.String(), "\x1b[") || json.Unmarshal([]byte(lines[0]), &record) != nil ||
			record.Event != "hotline.request" || record.Attributes["http.request.method"] != method {
			return failf("the console is %q", sut.printed.String())
		}
		return nil
	})
	sc.Step(`^the time the call took is yellow$`, func() error {
		sut.proxy.settle()
		if !regexp.MustCompile(`\x1b\[33m\d+\.\d ms\x1b\[0m`).MatchString(sut.printed.String()) {
			return failf("the console is %q", sut.printed.String())
		}
		return nil
	})
}

// setEnv sets one variable of the proxy's environment.
func (sut *proxySUT) setEnv(name, value string) {
	if sut.env == nil {
		sut.env = map[string]string{}
	}
	sut.env[name] = value
}

// consoleLines are the console's lines without their colours, once the
// proxy has printed everything.
func (sut *consoleSUT) consoleLines() []string {
	sut.proxy.settle()
	text := strings.TrimSuffix(ansi.ReplaceAllString(sut.printed.String(), ""), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// consoleLine checks one line, without its colours, against a pattern.
func (sut *consoleSUT) consoleLine(i int, pattern string) error {
	lines := sut.consoleLines()
	if len(lines) <= i || !regexp.MustCompile(pattern).MatchString(lines[i]) {
		return failf("line %d of the console does not match %q: %q", i+1, pattern, sut.printed.String())
	}
	return nil
}

// consoleHas checks that the console printed text, colours and all.
func (sut *consoleSUT) consoleHas(text string) error {
	sut.proxy.settle()
	if !strings.Contains(sut.printed.String(), text) {
		return failf("the console has no %q: %q", text, sut.printed.String())
	}
	return nil
}

// consoleSUT is the console exporter: what the proxy prints for each call, and
// the terminal it prints to.
type consoleSUT struct {
	proxy   *proxySUT
	vendors *vendorsSUT
	app     *appSUT
	// printed is what a console exporter printed, consoleExporter the
	// config's console entries, and terminal what standard output is.
	printed         *syncBuffer
	consoleExporter string
	terminal        fakeTerminal
	consoleClosed   bool
	// consoleJSON is --format json: each call is one line of JSON.
	consoleJSON bool
	// noConsole starts the proxy with no console option at all, as a Go
	// program that leaves it out does.
	noConsole bool
}

func newConsoleSUT(root *proxySUT, vendors *vendorsSUT, app *appSUT) *consoleSUT {
	sut := &consoleSUT{proxy: root, vendors: vendors, app: app, printed: &syncBuffer{}}
	root.addConfig(configExporters, func() string { return sut.consoleExporter })
	root.addOption(func(o *proxy.Options) { o.Console = sut.consoleOption() })
	return sut
}

// AddSteps adds the steps of the console exporter.
func (sut *consoleSUT) AddSteps(sc *godog.ScenarioContext) {
	consoleSteps(sc, sut)
}

// addExporter adds a console exporter with no settings, and text is what
// the console exporters printed.
func (sut *consoleSUT) addExporter() { sut.consoleExporter = "  - console: {}\n" }
func (sut *consoleSUT) text() string { return sut.printed.String() }
