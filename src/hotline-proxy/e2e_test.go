//go:build e2e

package proxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cucumber/godog"
)

// TestEndToEnd runs the scenarios tagged @e2e against the built
// hotline-proxy program. It runs only with the e2e build tag:
//
//	go test -tags e2e -run TestEndToEnd ./proxy/
//
// It builds the program itself, unless HOTLINE_PROXY names one already
// built (make e2e builds one that records coverage).
func TestEndToEnd(t *testing.T) {
	binary := os.Getenv("HOTLINE_PROXY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "hotline-proxy")
		build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "hotline-proxy/cmd") //nolint:gosec // builds the program under test
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("building hotline-proxy failed: %v\n%s", err, out)
		}
	}
	app := &exampleApp{dir: t.TempDir()}
	suite := godog.TestSuite{
		Name: "hotline-proxy-e2e",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			p := programScenario(sc, binary)
			snsScenario(sc, p, app)
		},
		Options: &godog.Options{
			Format:   "progress",
			Paths:    []string{"features"},
			Tags:     "@e2e && ~@later",
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("end-to-end feature files failed")
	}
}

// program is one scenario's system under test: the hotline-proxy program
// as a separate process, with a local TLS test vendor and a fake OTLP
// receiver at its edges. It is reached only through its command line, its
// output, its exit code and the network.
type program struct {
	binary   string
	dir      string
	issuer   *issuer
	receiver *receiver
	// vendor is the test vendor at localhost; untrusted is one whose
	// certificate no issuer vouches for.
	vendor    *vendor
	vendorURL string
	untrusted string
	proxyAddr string
	adminAddr string
	taken     net.Listener
	// tls makes the program's proxy port accept only TLS, with a
	// certificate for hotline-proxy from the test issuer.
	tls bool
	// pprof turns on telemetry.pprof, the profile pages on the admin port.
	pprof bool
	// interception, when set, is the issuer in the issuers section, which
	// listen.connect names, or listen.intercept when redirect is set.
	interception *issuer
	redirect     bool
	interceptAt  string
	// console adds a console exporter, and terminal, when set, is the
	// terminal the program prints to instead of standard output.
	console  bool
	terminal *terminal
	// cleanups undo what the scenario changed outside the program.
	cleanups []func()

	process  *exec.Cmd
	stdout   syncBuffer
	stderr   syncBuffer
	exited   chan struct{}
	exitCode int
	// signalled is when the last signal was sent.
	signalled time.Time
	exitedAt  time.Time

	// release, when closed, lets a vendor that holds its answer reply.
	release     chan struct{}
	releaseOnce sync.Once
	// arrived gets one value for each call that reaches a holding vendor.
	arrived chan struct{}

	resp     *http.Response
	respBody []byte
	respErr  error
	answered chan struct{}
}

func programScenario(sc *godog.ScenarioContext, binary string) *program {
	p := &program{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		dir, err := os.MkdirTemp("", "hotline-proxy-e2e-")
		*p = program{
			binary: binary, dir: dir, issuer: newIssuer(), proxyAddr: freePort(), adminAddr: freePort(),
			release: make(chan struct{}), arrived: make(chan struct{}, 8),
		}
		return ctx, err
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		p.close()
		return ctx, err
	})

	overlayCommandSteps(sc, p)
	consoleProgramSteps(sc, p)

	// Running the program.
	sc.Step(`^hotline-proxy is run with "([^"]*)"$`, func(args string) error {
		if err := p.startProcess(strings.Fields(args)...); err != nil {
			return err
		}
		return p.waitForExit(10 * time.Second)
	})
	sc.Step(`^the telemetry section turns on the profile pages$`, func() error {
		p.pprof = true
		return nil
	})
	sc.Step(`^the hotline-proxy program runs with integration test-shop for the test vendor$`, func() error {
		return p.run()
	})
	sc.Step(`^the hotline-proxy program runs with TLS on its proxy port and integration test-shop for the test vendor$`, func() error {
		p.tls = true
		return p.run()
	})
	sc.Step(`^the hotline-proxy program runs with listen.connect( and TLS on its port)? and integration test-shop for the test vendor$`, func(withTLS string) error {
		p.interception = newIssuer()
		p.tls = withTLS != ""
		return p.run()
	})
	sc.Step(`^the hotline-proxy program runs with listen.intercept and integration test-shop for the test vendor$`, func() error {
		if err := redirectAvailable(); err != nil {
			return godog.ErrSkip
		}
		p.interception, p.redirect, p.interceptAt = newIssuer(), true, freePort()
		return p.run()
	})
	sc.Step(`^the Go client sends POST to the test vendor's https address while a firewall redirects its connection to the listener$`, func() error {
		resp, err := p.goRedirected()
		p.respond(resp, err)
		return p.respErr
	})
	sc.Step(`^the Go client sends POST to the test vendor's https address through the proxy at an (http|https) one$`, func(scheme string) error {
		p.respond(p.goConnect(scheme, p.vendorURL+paymentPath, "test-shop"))
		return p.respErr
	})
	sc.Step(`^the hotline-proxy program runs with "([^"]*)" and integration test-shop for the test vendor$`, func(flags string) error {
		return p.run(strings.Fields(flags)...)
	})
	sc.Step(`^the program gets (SIGTERM|SIGINT|SIGHUP)$`, p.signal)
	sc.Step(`^the program gets (SIGTERM|SIGINT) again$`, p.signalAgain)
	sc.Step(`^it exits with code (\d+)$`, func(code int) error {
		return p.exitsWith(code, 30*time.Second)
	})
	sc.Step(`^it exits with code (\d+) within (\d+) seconds$`, func(code, seconds int) error {
		if err := p.exitsWith(code, time.Duration(seconds)*time.Second); err != nil {
			return err
		}
		if took := p.exitedAt.Sub(p.signalled); took > time.Duration(seconds)*time.Second {
			return failf("it took %s to exit after the signal", took)
		}
		return nil
	})

	// Building the program the way the image does.
	sc.Step(`^hotline-proxy is built with version "([^"]*)"(?: and commit "([^"]*)")?$`, func(v, commit string) error {
		p.binary = filepath.Join(p.dir, "hotline-proxy")
		// -buildvcs=false: the commit is the one given here, not the checkout's.
		ldflags := "-X main.buildVersion=" + v
		if commit != "" {
			ldflags += " -X main.buildCommit=" + commit
		}
		args := []string{"build", "-buildvcs=false", "-o", p.binary, "-ldflags", ldflags}
		if os.Getenv("GOCOVERDIR") != "" {
			// make e2e: this program records its coverage like the main one.
			args = append(args, "-cover", "-covermode=atomic", "-coverpkg=./...")
		}
		build := exec.CommandContext(context.Background(), "go", append(args, "hotline-proxy/cmd")...) //nolint:gosec // builds the program under test
		if out, err := build.CombinedOutput(); err != nil {
			return failf("building hotline-proxy failed: %v\n%s", err, out)
		}
		return nil
	})

	// Its output.
	sc.Step(`^standard output is "([^"]*)"$`, func(text string) error {
		if got := strings.TrimSuffix(p.stdout.String(), "\n"); got != text {
			return failf("standard output is %q", p.stdout.String())
		}
		return nil
	})
	sc.Step(`^standard output starts with "([^"]*)"$`, func(text string) error {
		if !strings.HasPrefix(p.stdout.String(), text) {
			return failf("standard output is %q", p.stdout.String())
		}
		return nil
	})
	sc.Step(`^standard output is the JSON (.+)$`, func(text string) error {
		if strings.TrimSuffix(p.stdout.String(), "\n") != text {
			return failf("standard output is %q", p.stdout.String())
		}
		return nil
	})
	sc.Step(`^standard error says "(.*)"$`, func(text string) error {
		if !strings.Contains(p.stderr.String(), text) {
			return failf("standard error does not say %q; it is:\n%s", text, p.stderr.String())
		}
		return nil
	})
	sc.Step(`^standard error does not say "(.*)"$`, func(text string) error {
		if strings.Contains(p.stderr.String(), text) {
			return failf("standard error says %q; it is:\n%s", text, p.stderr.String())
		}
		return nil
	})
	sc.Step(`^every line on standard error is a plain line with time, level and message$`, func() error {
		for _, line := range strings.Split(strings.TrimSuffix(p.stderr.String(), "\n"), "\n") {
			if !regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d{3} (DEBUG|INFO |WARN |ERROR) `).MatchString(line) {
				return failf("the line %q on standard error is not plain", line)
			}
		}
		return nil
	})
	sc.Step(`^every line on standard error is a JSON object with time, level and msg$`, func() error {
		lines := 0
		scanner := bufio.NewScanner(strings.NewReader(p.stderr.String()))
		for scanner.Scan() {
			lines++
			var line map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
				return failf("the line %q is not JSON: %v", scanner.Text(), err)
			}
			if err := hasAll(line, "time", "level", "msg"); err != nil {
				return err
			}
		}
		if lines == 0 {
			return failf("standard error is empty")
		}
		return nil
	})

	// Its config and trust file.
	sc.Step(`^a valid config file$`, func() error {
		p.writeConfig(p.config(true))
		return nil
	})
	sc.Step(`^a config file with no proxy listen address and no exporters$`, func() error {
		p.writeConfig(fmt.Sprintf("listen:\n  admin: %q\n", p.adminAddr))
		return nil
	})
	sc.Step(`^a config file whose listen.tls key file does not exist$`, func() error {
		p.tls = true
		p.writeConfig(p.config(true))
		return os.Remove(filepath.Join(p.dir, "tls", "key.pem"))
	})
	sc.Step(`^no file trust.pem$`, func() error { return nil })
	sc.Step(`^a file trust.pem that holds no certificate$`, func() error {
		return os.WriteFile(filepath.Join(p.dir, "trust.pem"), []byte("not a certificate\n"), 0o600)
	})
	sc.Step(`^something else listens on the config's proxy address$`, func() error {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", p.proxyAddr)
		p.taken = l
		return err
	})
	sc.Step(`^nothing listens on the config's proxy address$`, func() error {
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.proxyAddr)
		if err == nil {
			_ = conn.Close()
			return failf("something listens on %s", p.proxyAddr)
		}
		return nil
	})

	// The fakes at its edges.
	sc.Step(`^a fake OTLP receiver$`, func() error {
		p.receiver = newReceiver()
		return nil
	})
	sc.Step(`^a fake OTLP receiver that refuses connections$`, func() error {
		p.receiver = newReceiver()
		p.receiver.stop()
		return nil
	})
	sc.Step(`^the test vendor holds its answer$`, func() error {
		p.vendor.setAnswer(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			p.arrived <- struct{}{}
			<-p.release
			w.Header().Set("X-Request-Id", "req-5521")
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		return nil
	})
	sc.Step(`^an app's call is in progress and the program gets (SIGTERM|SIGINT)$`, func(name string) error {
		if err := p.callInProgress(); err != nil {
			return err
		}
		return p.signal(name)
	})
	sc.Step(`^the test vendor then answers$`, func() error {
		p.releaseVendor()
		return nil
	})
	sc.Step(`^the app gets the test vendor's answer$`, func() error {
		<-p.answered
		if p.respErr != nil {
			return p.respErr
		}
		if p.resp.StatusCode != http.StatusOK || string(p.respBody) != `{"ok":true}` {
			return failf("the app got %d %s", p.resp.StatusCode, p.respBody)
		}
		return nil
	})
	sc.Step(`^the receiver got the call's record$`, func() error {
		records, _ := p.receiver.got()
		if len(records) != 1 {
			return failf("the receiver got %d records, want 1", len(records))
		}
		return has(records[0].attrs, "hotline.integration.id", "test-shop")
	})

	// Reloading.
	sc.Step(`^an app's call is in progress$`, p.callInProgress)
	sc.Step(`^the config file changes$`, func() error {
		p.writeConfig(p.config(false) + "# changed\n")
		return nil
	})
	sc.Step(`^within (\d+) seconds standard error says "(.*)"$`, func(seconds int, text string) error {
		for deadline := time.Now().Add(time.Duration(seconds) * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if strings.Contains(p.stderr.String(), text) {
				return nil
			}
		}
		return failf("standard error does not say %q:\n%s", text, p.stderr.String())
	})
	// A call's record is made just after its answer is sent, so the
	// records of two calls in a row can reach the receiver in either order.
	sc.Step(`^the records carry two different config fingerprints$`, func() error {
		got, err := p.configLabels()
		if err != nil {
			return err
		}
		if len(got) != 2 || got[0] == got[1] {
			return failf("the records carry hotline.config.version %v, want two different ones", got)
		}
		return nil
	})
	sc.Step(`^the records carry one config fingerprint, not the first one$`, func() error {
		got, err := p.configLabels()
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0] == p.startLabel() {
			return failf("the records carry hotline.config.version %v, want one that is not %s", got, p.startLabel())
		}
		return nil
	})

	// The health checks.
	sc.Step(`^(GET|HEAD|POST) (\S+) on the admin port answers (\d+)$`, func(method, path string, status int) error {
		// A signal reaches the program a moment after it is sent, so the
		// answer may take that moment to change.
		var got int
		for deadline := time.Now().Add(time.Second); ; time.Sleep(20 * time.Millisecond) {
			var err error
			if got, err = p.askAdmin(method, path); err != nil {
				return err
			}
			if got == status || time.Now().After(deadline) {
				break
			}
		}
		if got != status {
			return failf("%s %s answered %d, want %d", method, path, got, status)
		}
		return nil
	})
	sc.Step(`^GET (\S+) on the admin port is asked (\d+) times$`, func(path string, times int) error {
		for range times {
			if _, err := p.askAdmin(http.MethodGet, path); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^a new call through the proxy reaches the test vendor$`, func() error {
		p.respond(goCase2(p.proxyAddr, strings.Replace(p.vendorURL, "https:", "http:", 1)+paymentPath, "test-shop"))
		if p.respErr != nil {
			return p.respErr
		}
		if p.resp.StatusCode != http.StatusOK {
			return failf("the call got %d %s", p.resp.StatusCode, p.respBody)
		}
		return nil
	})
	sc.Step(`^the proxy port stops taking calls between (\d+) and (\d+) seconds after the signal$`, func(from, to int) error {
		for deadline := p.signalled.Add(time.Duration(to) * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.proxyAddr)
			if err != nil {
				if took := time.Since(p.signalled); took < time.Duration(from)*time.Second {
					return failf("the proxy port stopped taking calls after %s", took)
				}
				return nil
			}
			_ = conn.Close()
		}
		return failf("the proxy port still takes calls %d seconds after the signal", to)
	})
	sc.Step(`^the receiver got no records$`, func() error {
		if records, _ := p.receiver.got(); len(records) != 0 {
			return failf("the receiver got %d records", len(records))
		}
		return nil
	})

	// The client matrix.
	sc.Step(`^the Go client sends case (\d+): .*$`, func(n int) error {
		switch n {
		case 1:
			p.respond(goCase1(p.proxyAddr, p.vendorURL+paymentPath, "test-shop"))
		case 2:
			p.respond(goCase2(p.proxyAddr, strings.Replace(p.vendorURL, "https:", "http:", 1)+paymentPath, "test-shop"))
		case 4:
			p.respond(goCase4(p.proxyAddr, p.vendorURL+paymentPath, "test-shop", p.appTLS()))
		case 5:
			p.respond(goCase5(p.proxyAddr, strings.Replace(p.vendorURL, "https:", "http:", 1)+paymentPath, "test-shop", p.appTLS()))
		default:
			return godog.ErrPending
		}
		return p.respErr
	})
	sc.Step(`^the Go client calls (a vendor with an untrusted certificate|a host that does not exist|the test vendor without Proxy-Authorization)$`, func(target string) error {
		switch target {
		case "a vendor with an untrusted certificate":
			p.respond(goCase1(p.proxyAddr, p.untrusted+paymentPath, "test-shop"))
		case "a host that does not exist":
			// .invalid is a name kept by RFC 6761 that never exists.
			p.respond(goCase1(p.proxyAddr, "https://nothing.invalid"+paymentPath, "test-shop"))
		default:
			p.respond(goCase1(p.proxyAddr, p.vendorURL+paymentPath, ""))
		}
		return p.respErr
	})
	sc.Step(`^the app gets the test vendor's status, headers and body unchanged$`, func() error {
		if p.resp.StatusCode != http.StatusOK || p.resp.Header.Get("X-Request-Id") != "req-5521" ||
			string(p.respBody) != `{"ok":true}` {
			return failf("the app got %d %v %s", p.resp.StatusCode, p.resp.Header, p.respBody)
		}
		return nil
	})
	sc.Step(`^the test vendor got the call over https and never saw Proxy-Authorization$`, func() error {
		calls := p.vendor.received()
		if len(calls) != 1 {
			return failf("the test vendor got %d calls, want 1", len(calls))
		}
		if calls[0].scheme != "https" || calls[0].header.Get("Proxy-Authorization") != "" {
			return failf("the test vendor got %s with headers %v", calls[0].scheme, calls[0].header)
		}
		if string(calls[0].body) != "amount=1000&currency=eur" {
			return failf("the test vendor got the body %q", calls[0].body)
		}
		return nil
	})
	sc.Step(`^the Go client sends POST (over TLS )?to the proxy with the (https?) vendor address in the path$`, func(overTLS, scheme string) error {
		vendor := strings.Replace(p.vendorURL, "https:", scheme+":", 1)
		if overTLS == "" {
			p.respond(goInPath("http://"+p.proxyAddr+"/"+vendor+paymentPath, "test-shop", nil))
		} else {
			p.respond(goInPath("https://"+p.proxyAddr+"/"+vendor+paymentPath, "test-shop", p.appTLS()))
		}
		return p.respErr
	})
	sc.Step(`^the Go client sends POST to the proxy with the test vendor's name in Host, over (plain HTTP/1.1|plain HTTP/2|HTTP/2 with TLS)$`, func(how string) error {
		host := strings.TrimPrefix(p.vendorURL, "https://")
		switch how {
		case "plain HTTP/1.1":
			p.respond(goHostHeader("http://"+p.proxyAddr+paymentPath, host, "test-shop", nil, false))
		case "plain HTTP/2":
			p.respond(goHostHeader("http://"+p.proxyAddr+paymentPath, host, "test-shop", nil, true))
		default:
			p.respond(goHostHeader("https://"+p.proxyAddr+paymentPath, host, "test-shop", p.appTLS(), true))
		}
		if p.respErr == nil && how != "plain HTTP/1.1" && p.resp.ProtoMajor != 2 {
			return failf("the app used %s, want HTTP/2", p.resp.Proto)
		}
		return p.respErr
	})
	sc.Step(`^the Go client sends POST to the test vendor's name over http, with that name pointing at the proxy, over (plain HTTP/1.1|plain HTTP/2)$`, func(how string) error {
		target := strings.Replace(p.vendorURL, "https:", "http:", 1) + paymentPath
		p.respond(goRedirected(target, p.proxyAddr, "test-shop", how == "plain HTTP/2"))
		if p.respErr == nil && how == "plain HTTP/2" && p.resp.ProtoMajor != 2 {
			return failf("the app used %s, want HTTP/2", p.resp.Proto)
		}
		return p.respErr
	})
	sc.Step(`^the test vendor got its own name in Host$`, func() error {
		calls := p.vendor.received()
		if want := strings.TrimPrefix(p.vendorURL, "https://"); len(calls) != 1 || calls[0].host != want {
			return failf("the test vendor got %+v, want one call with Host %s", calls, want)
		}
		return nil
	})
	sc.Step(`^the receiver got one hotline.request record with hotline.ingress (forward_proxy|url_in_path|host_header|connect|intercept) and ID test-shop$`, func(ingress string) error {
		record, err := p.record()
		if err != nil {
			return err
		}
		if err := has(record, "hotline.ingress", ingress); err != nil {
			return err
		}
		return has(record, "hotline.integration.id", "test-shop")
	})
	sc.Step(`^the receiver saw hotline.requests go up by one$`, func() error {
		if err := p.stop(); err != nil {
			return err
		}
		_, counts := p.receiver.got()
		if len(counts) == 0 {
			return failf("no count arrived")
		}
		if last := counts[len(counts)-1]; last.value != 1 {
			return failf("hotline.requests is %d, want 1", last.value)
		}
		return nil
	})
	sc.Step(`^the record holds the redacted url.full and no secret$`, func() error {
		record, err := p.record()
		if err != nil {
			return err
		}
		if err := has(record, "url.full", p.vendorURL+"/v1/payment_intents?expand=customer&customer=REDACTED"); err != nil {
			return err
		}
		for key, value := range record {
			if strings.Contains(fmt.Sprint(value), "cus_8812") || strings.Contains(fmt.Sprint(value), "test-shop:") {
				return failf("the record keeps a secret in %s: %v", key, value)
			}
		}
		return nil
	})
	sc.Step(`^the app gets 502 with Proxy-Status and the record's error.type is (\S+)$`, func(errorType string) error {
		if p.resp.StatusCode != http.StatusBadGateway || !strings.Contains(p.resp.Header.Get("Proxy-Status"), "error="+errorType) {
			return failf("the app got %d with Proxy-Status %q", p.resp.StatusCode, p.resp.Header.Get("Proxy-Status"))
		}
		record, err := p.record()
		if err != nil {
			return err
		}
		return has(record, "error.type", errorType)
	})
	sc.Step(`^the call reaches the vendor and the record is minimal with ID not-passed$`, func() error {
		if len(p.vendor.received()) != 1 || p.resp.StatusCode != http.StatusOK {
			return failf("the call did not reach the test vendor; the app got %d", p.resp.StatusCode)
		}
		record, err := p.record()
		if err != nil {
			return err
		}
		if err := has(record, "hotline.integration.id", "not-passed"); err != nil {
			return err
		}
		return hasNo(record, "url.full", "url.template")
	})
	return p
}

// paymentPath is the call the research clients make, with a customer ID
// the spec says to redact.
const paymentPath = "/v1/payment_intents?expand=customer&customer=cus_8812"

// testVendorSpec is our copy of the test vendor's spec.
const testVendorSpec = `openapi: 3.1.0
servers:
  - url: https://localhost
paths:
  /v1/payment_intents:
    post:
      parameters:
        - { name: expand, in: query, schema: { type: string }, x-hotline-redact: false }
        - { name: customer, in: query, schema: { type: string } }
      responses:
        "200": { description: A payment intent }
`

// run starts the program with the test vendor's config, trusting the test
// issuer, and waits until its proxy port takes calls.
func (p *program) run(flags ...string) error {
	p.startVendors()
	// A port that was free a moment ago can be taken by another program
	// on a busy computer before ours binds it. When the program says so,
	// the test picks new ports and starts it again.
	var err error
	for range 3 {
		p.pickPorts()
		p.writeConfig(p.config(false))
		if err = p.launch(flags...); err == nil || !strings.Contains(p.stderr.String(), "address already in use") {
			return err
		}
		p.stdout.Reset()
		p.stderr.Reset()
	}
	return err
}

// pickPorts chooses the program's ports again, once the test's own
// servers listen. A port chosen before them can be handed to one of them,
// and the program's calls would then reach the test vendor directly.
func (p *program) pickPorts() {
	p.proxyAddr, p.adminAddr = freePort(), freePort()
}

// launch starts the program with the config already written, trusting the
// test issuer, and waits until its proxy port listens.
func (p *program) launch(flags ...string) error {
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.issuer.cert.Raw})
	if err := os.WriteFile(filepath.Join(p.dir, "issuer.pem"), block, 0o600); err != nil {
		return err
	}
	args := append([]string{"run", "--config", "hotline-proxy.yaml", "--trust-file", "issuer.pem"}, flags...)
	if !slices.Contains(flags, "--drain-delay") {
		// Scenarios about other things stop at once, not after the
		// usual wait for Kubernetes.
		args = append(args, "--drain-delay", "0s")
	}
	if err := p.startProcess(args...); err != nil {
		return err
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", p.proxyAddr)
		if err == nil {
			return conn.Close()
		}
		select {
		case <-p.exited:
			return failf("the program exited with %d:\n%s", p.exitCode, p.stderr.String())
		default:
		}
	}
	return failf("the program did not listen on %s:\n%s", p.proxyAddr, p.stderr.String())
}

// askAdmin sends one request to the admin port and gives its status.
func (p *program) askAdmin(method, path string) (int, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+p.adminAddr+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (p *program) startVendors() {
	p.vendor = &vendor{host: "localhost"}
	trusted := httptest.NewUnstartedServer(p.vendor.record("https", ""))
	trusted.EnableHTTP2 = true
	trusted.TLS = &tls.Config{Certificates: []tls.Certificate{p.issuer.leaf("localhost", time.Now().Add(24*time.Hour), false)}}
	trusted.StartTLS()
	untrusted := httptest.NewUnstartedServer(p.vendor.record("https", ""))
	untrusted.TLS = &tls.Config{Certificates: []tls.Certificate{p.issuer.leaf("localhost", time.Now().Add(24*time.Hour), true)}}
	untrusted.StartTLS()
	p.vendor.servers = append(p.vendor.servers, trusted, untrusted)
	p.vendorURL = "https://localhost:" + port(trusted)
	p.untrusted = "https://localhost:" + port(untrusted)
}

func port(s *httptest.Server) string {
	_, port, _ := net.SplitHostPort(s.Listener.Addr().String())
	return port
}

// config is a config for the test vendor. A config only to validate needs
// no receiver.
func (p *program) config(validateOnly bool) string {
	endpoint := "http://127.0.0.1:4318"
	if !validateOnly {
		endpoint = p.receiver.endpoint
	}
	_ = os.MkdirAll(filepath.Join(p.dir, "specs"), 0o750)
	_ = os.WriteFile(filepath.Join(p.dir, "specs", "test-vendor.yaml"), []byte(testVendorSpec), 0o600)
	listenTLS := ""
	if p.tls {
		cert := p.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		_ = os.MkdirAll(filepath.Join(p.dir, "tls"), 0o750)
		_ = os.WriteFile(filepath.Join(p.dir, "tls", "cert.pem"), []byte(certPEM(cert)), 0o600)
		_ = os.WriteFile(filepath.Join(p.dir, "tls", "key.pem"), []byte(keyPEM(cert)), 0o600)
		listenTLS = "  tls: { cert: ./tls/cert.pem, key: ./tls/key.pem }\n"
	}
	if p.interception != nil {
		_ = os.MkdirAll(filepath.Join(p.dir, "ca"), 0o750)
		_ = os.WriteFile(filepath.Join(p.dir, "ca", "cert.pem"), []byte(issuerCertPEM(p.interception)), 0o600)
		_ = os.WriteFile(filepath.Join(p.dir, "ca", "key.pem"), []byte(issuerKeyPEM(p.interception)), 0o600)
		if p.redirect {
			listenTLS += fmt.Sprintf("  intercept: { address: %q, issuer: customer, integration: test-shop }\n", p.interceptAt)
		} else {
			listenTLS += "  connect: { issuer: customer }\n"
		}
		listenTLS += "issuers:\n  customer: { cert: ./ca/cert.pem, key: ./ca/key.pem }\n"
	}
	telemetry := ""
	if p.console {
		telemetry = "  - console: {}\n"
	}
	if p.pprof {
		telemetry += "telemetry:\n  pprof: true\n"
	}
	// The test vendor runs on this computer, an internal address, so the
	// config allows internal IPs.
	return fmt.Sprintf(`listen:
  proxy: %q
  admin: %q
%sexporters:
  - otlp:
      endpoint: %s
      metricInterval: 100ms
      logInterval: 100ms
      traceInterval: 100ms
%sapis:
  test-vendor:
    hosts: [localhost]
    spec: ./specs/test-vendor.yaml
    allowInternalIPs: true
integrations:
  - id: test-shop
    apis: [test-vendor]
  - id: not-passed
    allowInternalIPs: true
`, p.proxyAddr, p.adminAddr, listenTLS, endpoint, telemetry)
}

// configLabels are the hotline.config.version of each record, each checked
// to be the config's file name, a + and a fingerprint.
func (p *program) configLabels() ([]string, error) {
	records, _ := p.receiver.got()
	got := make([]string, 0, len(records))
	for _, r := range records {
		label, _ := r.attrs["hotline.config.version"].(string)
		if !regexp.MustCompile(`^hotline-proxy\.yaml\+[0-9a-f]{12}$`).MatchString(label) {
			return nil, failf("a record carries hotline.config.version %q, want hotline-proxy.yaml+ and a fingerprint", label)
		}
		got = append(got, label)
	}
	slices.Sort(got)
	return got, nil
}

// startLabel is the config_version the program logged when it started.
func (p *program) startLabel() string {
	m := regexp.MustCompile(`hotline proxy started.*config_version=(\S+)`).FindStringSubmatch(p.stderr.String())
	if m == nil {
		return ""
	}
	return m[1]
}

func (p *program) writeConfig(content string) {
	_ = os.WriteFile(filepath.Join(p.dir, "hotline-proxy.yaml"), []byte(content), 0o600)
}

func (p *program) startProcess(args ...string) error {
	p.process = exec.CommandContext(context.Background(), p.binary, args...) //nolint:gosec // the program under test
	p.process.Dir = p.dir
	p.process.Stdout = &p.stdout
	if p.terminal != nil {
		p.process.Stdout = p.terminal.program
	}
	p.process.Stderr = &p.stderr
	if err := p.process.Start(); err != nil {
		return err
	}
	p.exited = make(chan struct{})
	go func() {
		_ = p.process.Wait()
		p.exitCode = p.process.ProcessState.ExitCode()
		p.exitedAt = time.Now()
		close(p.exited)
	}()
	return nil
}

func (p *program) waitForExit(limit time.Duration) error {
	select {
	case <-p.exited:
		return nil
	case <-time.After(limit):
		return failf("the program did not exit within %s", limit)
	}
}

func (p *program) exitsWith(code int, limit time.Duration) error {
	if err := p.waitForExit(limit); err != nil {
		return err
	}
	if p.exitCode != code {
		return failf("it exited with %d, want %d; standard error:\n%s", p.exitCode, code, p.stderr.String())
	}
	return nil
}

func (p *program) signal(name string) error {
	sig := map[string]syscall.Signal{"SIGTERM": syscall.SIGTERM, "SIGINT": syscall.SIGINT, "SIGHUP": syscall.SIGHUP}[name]
	p.signalled = time.Now()
	return p.process.Process.Signal(sig)
}

// signalAgain sends a second signal once the program has taken the first.
// Sent sooner, the system can merge the two into one.
func (p *program) signalAgain(name string) error {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(p.stderr.String(), "hotline proxy stopping") {
			return p.signal(name)
		}
	}
	return failf("the program did not take the first signal:\n%s", p.stderr.String())
}

// stop ends the program the way Kubernetes does, which sends every queued
// record, and checks that it stopped cleanly.
func (p *program) stop() error {
	select {
	case <-p.exited:
	default:
		if err := p.signal("SIGTERM"); err != nil {
			return err
		}
	}
	return p.exitsWith(0, 30*time.Second)
}

// record is the only record the receiver got, once the program stopped.
func (p *program) record() (map[string]any, error) {
	if err := p.stop(); err != nil {
		return nil, err
	}
	records, _ := p.receiver.got()
	if len(records) != 1 {
		return nil, fmt.Errorf("%w: want exactly one record, got %d", errNoRecord, len(records))
	}
	if records[0].eventName != "hotline.request" {
		return nil, failf("the record's event.name is %q", records[0].eventName)
	}
	return records[0].attrs, nil
}

func (p *program) respond(resp *http.Response, err error) {
	p.resp, p.respErr = resp, err
	if err == nil {
		p.respBody, p.respErr = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

// callInBackground makes a case 2 call that the test waits for later.
func (p *program) callInBackground() {
	p.answered = make(chan struct{})
	target := strings.Replace(p.vendorURL, "https:", "http:", 1) + paymentPath
	go func() {
		defer close(p.answered)
		p.respond(goCase2(p.proxyAddr, target, "test-shop"))
	}()
}

// callInProgress makes a case 2 call and waits until it reaches the
// holding test vendor.
func (p *program) callInProgress() error {
	p.callInBackground()
	select {
	case <-p.arrived:
		return nil
	case <-p.answered:
		return failf("the call ended before it reached the test vendor: %v\n%s", p.respErr, p.stderr.String())
	case <-time.After(5 * time.Second):
		return failf("the call did not reach the test vendor:\n%s", p.stderr.String())
	}
}

func (p *program) releaseVendor() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *program) close() {
	for _, undo := range p.cleanups {
		undo()
	}
	p.releaseVendor()
	if p.process != nil && p.process.ProcessState == nil {
		_ = p.process.Process.Kill()
		<-p.exited
	}
	if p.vendor != nil {
		p.vendor.close()
	}
	if p.receiver != nil {
		p.receiver.stop()
	}
	if p.taken != nil {
		_ = p.taken.Close()
	}
	_ = os.RemoveAll(p.dir)
}

// The Go client cases below are copied from research/matrix-clients/go.
// Case numbers are those in egress-proxy-options.md.

// goTransport is an HTTP/1.1-only transport: a full address in the request
// line does not exist in HTTP/2.
func goTransport(proxy *url.URL) *http.Transport {
	t := &http.Transport{TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return t
}

func paymentRequest(target string) *http.Request {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("amount=1000&currency=eur"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// goCase1 sends "POST https://… HTTP/1.1" over plain TCP to the proxy. Go
// would CONNECT for an https address, so the full address goes in
// URL.Opaque, and the dial for TLS returns a plain connection. An empty id
// sends no Proxy-Authorization.
func goCase1(proxyAddr, target, id string) (*http.Response, error) {
	t := goTransport(nil)
	t.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
	}
	defer t.CloseIdleConnections()
	req := paymentRequest(target)
	req.URL = &url.URL{Scheme: "https", Host: proxyAddr, Opaque: strings.TrimPrefix(target, "https:")}
	if id != "" {
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":x")))
	}
	return t.RoundTrip(req)
}

// goCase2 sends "POST http://… HTTP/1.1" through the standard proxy
// setting, which also sends the ID as Proxy-Authorization.
func goCase2(proxyAddr, target, id string) (*http.Response, error) {
	t := goTransport(&url.URL{Scheme: "http", Host: proxyAddr, User: url.UserPassword(id, "x")})
	defer t.CloseIdleConnections()
	return t.RoundTrip(paymentRequest(target))
}

// appTLS is how an app that trusts the test issuer opens TLS to the
// program, by its name hotline-proxy.
func (p *program) appTLS() *tls.Config {
	return &tls.Config{RootCAs: p.issuer.pool, ServerName: "hotline-proxy", MinVersion: tls.VersionTLS12}
}

// goCase4 sends "POST https://… HTTP/1.1" inside TLS to the proxy, the same
// way as goCase1 but with the dial for TLS opening TLS to the proxy.
func goCase4(proxyAddr, target, id string, appTLS *tls.Config) (*http.Response, error) {
	t := goTransport(nil)
	t.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&tls.Dialer{Config: appTLS}).DialContext(ctx, network, proxyAddr)
	}
	defer t.CloseIdleConnections()
	req := paymentRequest(target)
	req.URL = &url.URL{Scheme: "https", Host: proxyAddr, Opaque: strings.TrimPrefix(target, "https:")}
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":x")))
	return t.RoundTrip(req)
}

// goConnect calls the vendor's own https address through the standard
// proxy setting, as HTTPS_PROXY does: Go sends CONNECT with the ID from the
// proxy address, then sets up TLS with the "vendor" inside the tunnel. The
// app trusts the interception issuer, and the test issuer for the proxy's
// own name. Like Go's default client, it offers HTTP/2 everywhere.
func (p *program) goConnect(proxyScheme, target, id string) (*http.Response, error) {
	_, proxyPort, _ := net.SplitHostPort(p.proxyAddr)
	roots := p.interception.pool.Clone()
	roots.AddCert(p.issuer.cert)
	t := &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: proxyScheme, Host: "hotline-proxy:" + proxyPort, User: url.UserPassword(id, "x")}),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, p.proxyAddr)
		},
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	defer t.CloseIdleConnections()
	return t.RoundTrip(paymentRequest(target))
}

// goCase5 sends "POST http://… HTTP/1.1" through the standard proxy setting
// with an https proxy address, so Go opens TLS to the proxy first.
func goCase5(proxyAddr, target, id string, appTLS *tls.Config) (*http.Response, error) {
	t := goTransport(&url.URL{Scheme: "https", Host: proxyAddr, User: url.UserPassword(id, "x")})
	t.TLSClientConfig = appTLS
	defer t.CloseIdleConnections()
	return t.RoundTrip(paymentRequest(target))
}

// goInPath sends the call to the proxy's own address with the vendor's
// address in the path, the way an app does by changing only its base
// address. With appTLS set, the base address is https and the app opens TLS
// to the proxy.
func goInPath(target, id string, appTLS *tls.Config) (*http.Response, error) {
	t := goTransport(nil)
	t.TLSClientConfig = appTLS
	defer t.CloseIdleConnections()
	req := paymentRequest(target)
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":x")))
	return t.RoundTrip(req)
}

// goHostHeader sends the call to the proxy's own address with the vendor's
// name in the Host header, the way an app does by changing its base address
// and setting Host. With http2 set, it speaks HTTP/2: inside TLS when
// appTLS is set, otherwise plain from the first byte.
func goHostHeader(target, host, id string, appTLS *tls.Config, http2 bool) (*http.Response, error) {
	protocols := new(http.Protocols)
	switch {
	case !http2:
		protocols.SetHTTP1(true)
	case appTLS != nil:
		protocols.SetHTTP2(true)
	default:
		protocols.SetUnencryptedHTTP2(true)
	}
	t := &http.Transport{Protocols: protocols, TLSClientConfig: appTLS}
	defer t.CloseIdleConnections()
	req := paymentRequest(target)
	req.Host = host
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":x")))
	return t.RoundTrip(req)
}

// goRedirected calls the vendor's own address over plain http, the way an
// app does when only https became http in its base address. Its name lookup
// leads to the proxy, as a hostAliases entry in the app's pod would, and it
// sends Proxy-Authorization as a default header. With http2 set, it speaks
// HTTP/2 from the first byte.
func goRedirected(target, proxyAddr, id string, http2 bool) (*http.Response, error) {
	protocols := new(http.Protocols)
	if http2 {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP1(true)
	}
	t := &http.Transport{Protocols: protocols}
	t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
	}
	defer t.CloseIdleConnections()
	req := paymentRequest(target)
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":x")))
	return t.RoundTrip(req)
}

// redirectDestination is an address nobody answers at, so the only way the
// program's client reaches anything is the firewall redirect.
const redirectDestination = "192.0.2.77"

// firewall runs the firewall tool, through sudo when the test is not run
// as root.
func firewall(args ...string) ([]byte, error) {
	name := "iptables"
	if os.Geteuid() != 0 {
		name, args = "sudo", append([]string{"-n", "iptables"}, args...)
	}
	return exec.CommandContext(context.Background(), name, args...).CombinedOutput() //nolint:gosec // the test changes its own firewall
}

// redirectAvailable says whether the test can change the firewall.
func redirectAvailable() error {
	_, err := firewall("-t", "nat", "-L", "OUTPUT", "-n")
	return err
}

// goRedirected calls the test vendor's own https address from a Go client
// that knows nothing of the proxy. A firewall rule redirects its connection
// to the vendor's port, at an address nobody answers at, to the program's
// intercept listener; the client names the vendor in the TLS handshake, and
// the program reads the port from the operating system.
func (p *program) goRedirected() (*http.Response, error) {
	vendorPort := strings.TrimPrefix(p.vendorURL, "https://localhost:")
	_, listenerPort, _ := net.SplitHostPort(p.interceptAt)
	rule := []string{"-p", "tcp", "-d", redirectDestination, "--dport", vendorPort, "-j", "REDIRECT", "--to-ports", listenerPort}
	if out, err := firewall(append([]string{"-t", "nat", "-A", "OUTPUT"}, rule...)...); err != nil {
		return nil, fmt.Errorf("adding the firewall rule failed: %w: %s", err, out)
	}
	p.cleanups = append(p.cleanups, func() {
		_, _ = firewall(append([]string{"-t", "nat", "-D", "OUTPUT"}, rule...)...)
	})
	roots := p.interception.pool.Clone()
	roots.AddCert(p.issuer.cert)
	t := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(redirectDestination, vendorPort))
		},
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	defer t.CloseIdleConnections()
	return t.RoundTrip(paymentRequest(p.vendorURL + paymentPath))
}
