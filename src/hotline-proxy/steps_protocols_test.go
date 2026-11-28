package proxy_test

import (
	"crypto/tls"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

func protocolSteps(sc *godog.ScenarioContext, sut *otlpSUT) {
	var secret string
	// use replaces the first receiver with one set up as given.
	use := func(setup receiverSetup) {
		sut.receivers[0].stop()
		sut.receivers[0] = newReceiverWith(setup)
	}
	// overTLS starts a receiver over TLS, with a certificate for localhost
	// from signer.
	overTLS := func(signer *authority, change func(*tls.Config)) {
		cfg := &tls.Config{
			Certificates: []tls.Certificate{signer.leaf("localhost", time.Now().Add(24*time.Hour), false)},
			MinVersion:   tls.VersionTLS12,
		}
		if change != nil {
			change(cfg)
		}
		use(receiverSetup{tls: cfg})
	}
	sc.Step(`^a receiver$`, func() error { return nil })
	sc.Step(`^a receiver whose endpoint ends in (/\S+)$`, func(path string) error {
		use(receiverSetup{path: path})
		return nil
	})
	sc.Step(`^the receiver gets the record on /v1/logs and the spans on /v1/traces$`, func() error {
		sut.proxy.settle()
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		var paths []string
		for _, seen := range sut.receivers[0].requests() {
			paths = append(paths, seen.path)
		}
		if !slices.Contains(paths, "/v1/logs") || !slices.Contains(paths, "/v1/traces") {
			return failf("the receiver got requests to %v", paths)
		}
		return nil
	})
	sc.Step(`^every request is sent as application/x-protobuf$`, func() error {
		for _, seen := range sut.receivers[0].requests() {
			if seen.contentType != "application/x-protobuf" {
				return failf("a request to %s was sent as %q", seen.path, seen.contentType)
			}
		}
		return nil
	})
	sc.Step(`^the receiver gets the record on (/\S+)$`, func(path string) error {
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range sut.receivers[0].requests() {
			if seen.logs && seen.path == path {
				return nil
			}
		}
		return failf("no record came to %s; the receiver got %v", path, sut.receivers[0].requests())
	})

	sc.Step(`^a receiver over TLS whose certificate authority only tls.ca trusts$`, func() error {
		other := newAuthority()
		overTLS(other, nil)
		ca := sut.proxy.writeFile("otlp/ca.crt", authorityCertPEM(other))
		sut.exporterExtra = []string{"      tls:\n        ca: " + ca + "\n"}
		return nil
	})
	sc.Step(`^the receiver gets the record over TLS$`, func() error {
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range sut.receivers[0].requests() {
			if !seen.tls {
				return failf("a request came without TLS")
			}
		}
		return nil
	})
	sc.Step(`^a receiver over TLS whose certificate authority nobody trusts$`, func() error {
		overTLS(newAuthority(), nil)
		// Each send is tried again until its time runs out.
		sut.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the receiver gets no record$`, func() error {
		if got := len(sut.records()); got != 0 {
			return failf("the receiver got %d records, want none", got)
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that the send to that endpoint failed$`, func() error {
		sut.proxy.settle()
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "export failed") &&
				strings.Contains(line, sut.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the failed send; the log is %s", sut.proxy.logs.String())
	})
	sc.Step(`^a receiver over TLS that asks for a client certificate$`, func() error {
		overTLS(sut.vendors.authority, func(cfg *tls.Config) {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
			cfg.ClientCAs = sut.vendors.authority.pool
		})
		client := sut.vendors.authority.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		cert := sut.proxy.writeFile("otlp/client.crt", certPEM(client))
		key := sut.proxy.writeFile("otlp/client.key", keyPEM(client))
		sut.exporterExtra = []string{"      tls:\n        cert: " + cert + "\n        key: " + key + "\n"}
		return nil
	})
	sc.Step(`^the receiver saw the proxy's client certificate$`, func() error {
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range sut.receivers[0].requests() {
			if !seen.clientCert {
				return failf("a request came without a client certificate")
			}
		}
		return nil
	})

	sc.Step(`^the exporter sends the header Authorization from (a plain value|a file|an environment variable)$`, func(source string) error {
		secret = "Bearer s3cr3t-otlp-token"
		value := `"` + secret + `"`
		switch source {
		case "a file":
			// Secret files often end in a line break, which is not part
			// of the value.
			value = "{ file: " + sut.proxy.writeFile("otlp/token", secret+"\n") + " }"
		case "an environment variable":
			sut.proxy.setEnv("OTLP_TOKEN", secret)
			value = "{ env: OTLP_TOKEN }"
		}
		sut.exporterExtra = []string{"      headers:\n        Authorization: " + value + "\n"}
		return nil
	})
	sc.Step(`^every request to the receiver carried that Authorization value$`, func() error {
		sut.proxy.settle()
		seen := sut.receivers[0].requests()
		if len(seen) == 0 {
			return failf("the receiver got no request")
		}
		for _, s := range seen {
			if len(s.authorization) != 1 || s.authorization[0] != secret {
				return failf("a request carried Authorization %v", s.authorization)
			}
		}
		return nil
	})
	sc.Step(`^the value is in none of the proxy's log lines$`, func() error {
		sut.proxy.settle()
		if strings.Contains(sut.proxy.logs.String(), "s3cr3t-otlp-token") {
			return failf("the log holds the secret: %s", sut.proxy.logs.String())
		}
		return nil
	})

	sc.Step(`^a receiver and an exporter that (does not set compression|sets compression to none)$`, func(sets string) error {
		if sets == "sets compression to none" {
			sut.exporterExtra = []string{"      compression: none\n"}
		}
		return nil
	})
	sc.Step(`^every request to the receiver is (gzipped|plain)$`, func(encoding string) error {
		sut.proxy.settle()
		want := map[string]string{"gzipped": "gzip", "plain": ""}[encoding]
		seen := sut.receivers[0].requests()
		if len(seen) == 0 {
			return failf("the receiver got no request")
		}
		for _, s := range seen {
			if s.encoding != want {
				return failf("a request was sent with encoding %q, want %q", s.encoding, want)
			}
		}
		return nil
	})

	sc.Step(`^a receiver that answers the first log send with (\d+)(?: with Retry-After (\d+))?$`, func(status int, retryAfter string) error {
		sut.first().answerNextLogExports(answer{status: status, retryAfter: retryAfter})
		sut.exportTimeout = 5 * time.Second
		return nil
	})
	logSends := func() int {
		n := 0
		for _, seen := range sut.receivers[0].requests() {
			if seen.logs {
				n++
			}
		}
		return n
	}
	// sendsBeforeStop counts the log sends while the proxy runs, once ready
	// says so: a stop sends the ledger's last batch record too.
	sendsBeforeStop := func(ready func() bool) int {
		for deadline := time.Now().Add(waitLimit); !ready() && time.Now().Before(deadline); {
			pause(5 * time.Millisecond)
		}
		// Any retry would come at once: give it a moment.
		pause(50 * time.Millisecond)
		return logSends()
	}
	sc.Step(`^a receiver that answers the first (\d+) log sends with 503, each longer than a send may take$`, func(n int) error {
		for range n {
			sut.first().answerNextLogExports(answer{status: http.StatusServiceUnavailable})
		}
		// Shorter than the first wait before a retry, so each send gives up
		// after its first try.
		sut.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^a receiver that answers every log send with 503$`, func() error {
		for range 1000 {
			sut.first().answerNextLogExports(answer{status: http.StatusServiceUnavailable})
		}
		sut.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the record arrives on the third send$`, func() error {
		sends := sendsBeforeStop(func() bool { return len(sut.receivers[0].got()) > 0 })
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		if sends != 3 {
			return failf("the record was sent %d times, want 3", sends)
		}
		return nil
	})
	sc.Step(`^the proxy logs no warning about dropped records$`, func() error {
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "dropped") {
				return failf("the proxy warned about dropped records: %s", line)
			}
		}
		return nil
	})
	sc.Step(`^the record arrives on the second try$`, func() error {
		sends := sendsBeforeStop(func() bool { records := sut.receivers[0].got(); return len(records) > 0 })
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		if got := sends; got != 2 {
			return failf("the record was sent %d times, want 2", got)
		}
		return nil
	})
	sc.Step(`^the record is lost, and the proxy logs that 1 was dropped$`, func() error {
		sends := sendsBeforeStop(func() bool { return logSends() > 0 })
		if got := len(sut.records()); got != 0 {
			return failf("the receiver got %d records, want none", got)
		}
		if got := sends; got != 1 {
			return failf("the record was sent %d times, want 1", got)
		}
		for _, line := range strings.Split(sut.proxy.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "dropped=1") &&
				strings.Contains(line, sut.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the dropped record; the log is %s", sut.proxy.logs.String())
	})

	sc.Step(`^a receiver (over TLS|plain) that offers (HTTP/2|HTTP/1.1|h2c)$`, func(over, offers string) error {
		setup := receiverSetup{http2: offers == "HTTP/2", h2c: offers == "h2c"}
		if over == "over TLS" {
			setup.tls = &tls.Config{
				Certificates: []tls.Certificate{sut.vendors.authority.leaf("localhost", time.Now().Add(24*time.Hour), false)},
				MinVersion:   tls.VersionTLS12,
			}
		}
		use(setup)
		// A send that cannot be read is tried again until its time runs out.
		sut.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the exporter (does not set h2c|sets h2c to true)$`, func(sets string) error {
		if sets == "sets h2c to true" {
			sut.exporterExtra = []string{"      h2c: true\n"}
		}
		return nil
	})
	sc.Step(`^the receiver gets the record$`, func() error {
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		return nil
	})
	sc.Step(`^every request to the receiver came over (HTTP/2.0|HTTP/1.1)$`, func(version string) error {
		sut.proxy.settle()
		seen := sut.receivers[0].requests()
		if len(seen) == 0 {
			return failf("the receiver got no request")
		}
		for _, s := range seen {
			if s.proto != version {
				return failf("a request to %s came over %s, want %s", s.path, s.proto, version)
			}
		}
		return nil
	})

	sc.Step(`^OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS and OTEL_EXPORTER_OTLP_COMPRESSION are set to other values$`, func() error {
		// The OpenTelemetry library reads these from the process itself,
		// not from what the proxy is given.
		for name, value := range map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT":    "http://" + sut.proxy.ports.take(),
			"OTEL_EXPORTER_OTLP_HEADERS":     "x-from-env=leaked",
			"OTEL_EXPORTER_OTLP_COMPRESSION": "none",
		} {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
			sut.proxy.processEnv = append(sut.proxy.processEnv, name)
		}
		return nil
	})
	sc.Step(`^the receiver gets the record, gzipped, with no header from the environment$`, func() error {
		if got := len(sut.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range sut.receivers[0].requests() {
			if seen.encoding != "gzip" || seen.header.Get("X-From-Env") != "" {
				return failf("a request had encoding %q and headers %v", seen.encoding, seen.header)
			}
		}
		return nil
	})
}
