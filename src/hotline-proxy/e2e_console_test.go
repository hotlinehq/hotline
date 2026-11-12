//go:build e2e

package proxy_test

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// terminal is a terminal the program prints to: its end for the program,
// and what the test read from the other end.
type terminal struct {
	program *os.File
	reader  *os.File
	shown   syncBuffer
	done    chan struct{}
}

// text is what the terminal showed, with its line ends as written.
func (t *terminal) text() string {
	return strings.ReplaceAll(t.shown.String(), "\r\n", "\n")
}

func (t *terminal) close() {
	_ = t.program.Close()
	_ = t.reader.Close()
	// The reader may still wait; closing its file ends it.
}

func consoleProgramSteps(sc *godog.ScenarioContext, p *program) {
	sc.Step(`^the hotline-proxy program runs with a console exporter and integration test-shop for the test vendor$`, func() error {
		p.console = true
		return p.run()
	})
	sc.Step(`^the hotline-proxy program runs with "([^"]*)", a console exporter and integration test-shop for the test vendor$`, func(flags string) error {
		p.console = true
		return p.run(strings.Fields(flags)...)
	})
	sc.Step(`^standard output shows the call as one line of JSON$`, func() error {
		return waitFor(func() string { return p.stdout.String() }, `^\{"time":.*"event":"hotline\.request",.*"hotline\.integration\.id":"test-shop".*\}\n$`)
	})
	sc.Step(`^the hotline-proxy program runs on a terminal (\d+) columns wide, with a console exporter and integration test-shop for the test vendor$`, func(columns int) error {
		t, err := openTerminal(columns)
		if err != nil {
			return godog.ErrSkip
		}
		p.terminal, p.console = t, true
		p.cleanups = append(p.cleanups, t.close)
		return p.run()
	})
	sc.Step(`^the Go client calls the test vendor$`, func() error {
		p.respond(goCase1(p.proxyAddr, p.vendorURL+paymentPath, "test-shop"))
		return p.respErr
	})
	sc.Step(`^standard output shows the call as two plain lines$`, func() error {
		return waitFor(func() string { return p.stdout.String() }, `^\d\d:\d\d:\d\d\.\d\d\d test-shop: POST https://localhost:\d+/v1/payment_intents\S*\n +<< 200 OK `)
	})
	sc.Step(`^standard error has plain lines, from the start to the stop$`, func() error {
		lines := strings.Split(strings.TrimSuffix(p.stderr.String(), "\n"), "\n")
		plain := regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d{3} (DEBUG|INFO |WARN |ERROR) `)
		for _, line := range lines {
			if !plain.MatchString(line) {
				return failf("the line %q on standard error is not plain", line)
			}
		}
		if !strings.Contains(lines[len(lines)-1], "hotline proxy stopped") {
			return failf("standard error ends with %q", lines[len(lines)-1])
		}
		return nil
	})
	sc.Step(`^the terminal shows the call in colour$`, func() error {
		return waitFor(p.terminal.text, `\x1b\[36mtest-shop\x1b\[0m: \x1b\[1;35mPOST\x1b\[0m `)
	})
}

// waitFor waits up to 5 seconds for the text to match the pattern.
func waitFor(text func() string, pattern string) error {
	re := regexp.MustCompile(pattern)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if re.MatchString(text()) {
			return nil
		}
	}
	return failf("the output does not match %q: %q", pattern, text())
}
