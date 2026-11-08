package proxy_test

import (
	"crypto/tls"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func protocolSteps(sc *godog.ScenarioContext, w *world) {
	var secret string
	// use replaces the first receiver with one set up as given.
	use := func(setup receiverSetup) {
		w.receivers[0].stop()
		w.receivers[0] = newReceiverWith(setup)
	}
	// overTLS starts a receiver of the protocol over TLS, with a certificate
	// for localhost from signer.
	overTLS := func(protocol string, signer *issuer, change func(*tls.Config)) {
		cfg := &tls.Config{
			Certificates: []tls.Certificate{signer.leaf("localhost", time.Now().Add(24*time.Hour), false)},
			MinVersion:   tls.VersionTLS12,
		}
		if change != nil {
			change(cfg)
		}
		use(receiverSetup{http: protocol == "HTTP", tls: cfg})
	}
	sc.Step(`^an exporter with no protocol set$`, func() error { return nil })
	sc.Step(`^the receiver gets the record and the count over gRPC$`, func() error {
		if len(w.counts()) == 0 {
			return failf("no count arrived")
		}
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range w.receivers[0].requests() {
			if seen.path != "" {
				return failf("a request came over HTTP, to %s", seen.path)
			}
		}
		return nil
	})
	sc.Step(`^an? (gRPC|HTTP) receiver$`, func(protocol string) error {
		if protocol == "HTTP" {
			use(receiverSetup{http: true})
		}
		return nil
	})
	sc.Step(`^an HTTP receiver whose endpoint ends in (/\S+)$`, func(path string) error {
		use(receiverSetup{http: true, path: path})
		return nil
	})
	sc.Step(`^the receiver gets the record on /v1/logs and the count on /v1/metrics$`, func() error {
		if len(w.counts()) == 0 {
			return failf("no count arrived")
		}
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		var paths []string
		for _, seen := range w.receivers[0].requests() {
			paths = append(paths, seen.path)
		}
		if !slices.Contains(paths, "/v1/logs") || !slices.Contains(paths, "/v1/metrics") {
			return failf("the receiver got requests to %v", paths)
		}
		return nil
	})
	sc.Step(`^every request is sent as application/x-protobuf$`, func() error {
		for _, seen := range w.receivers[0].requests() {
			if seen.contentType != "application/x-protobuf" {
				return failf("a request to %s was sent as %q", seen.path, seen.contentType)
			}
		}
		return nil
	})
	sc.Step(`^the receiver gets the record on (/\S+)$`, func(path string) error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range w.receivers[0].requests() {
			if seen.logs && seen.path == path {
				return nil
			}
		}
		return failf("no record came to %s; the receiver got %v", path, w.receivers[0].requests())
	})
	sc.Step(`^a gRPC receiver and an HTTP receiver$`, func() error {
		w.receivers = append(w.receivers, newReceiverWith(receiverSetup{http: true}))
		return nil
	})

	sc.Step(`^an? (gRPC|HTTP) receiver over TLS whose issuer only tls.ca trusts$`, func(protocol string) error {
		other := newIssuer()
		overTLS(protocol, other, nil)
		ca := w.writeFile("otlp/ca.crt", issuerCertPEM(other))
		w.exporterExtra = []string{"      tls:\n        ca: " + ca + "\n"}
		return nil
	})
	sc.Step(`^the receiver gets the record over TLS$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range w.receivers[0].requests() {
			if !seen.tls {
				return failf("a request came without TLS")
			}
		}
		return nil
	})
	sc.Step(`^an? (gRPC|HTTP) receiver over TLS whose issuer nobody trusts$`, func(protocol string) error {
		overTLS(protocol, newIssuer(), nil)
		// Each send is tried again until its time runs out.
		w.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the receiver gets no record$`, func() error {
		if got := len(w.records()); got != 0 {
			return failf("the receiver got %d records, want none", got)
		}
		return nil
	})
	sc.Step(`^the proxy logs a warning that the send to that endpoint failed$`, func() error {
		w.settle()
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "export failed") &&
				strings.Contains(line, w.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the failed send; the log is %s", w.logs.String())
	})
	sc.Step(`^an? (gRPC|HTTP) receiver over TLS that asks for a client certificate$`, func(protocol string) error {
		overTLS(protocol, w.issuer, func(cfg *tls.Config) {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
			cfg.ClientCAs = w.issuer.pool
		})
		client := w.issuer.leaf("hotline-proxy", time.Now().Add(24*time.Hour), false)
		cert := w.writeFile("otlp/client.crt", certPEM(client))
		key := w.writeFile("otlp/client.key", keyPEM(client))
		w.exporterExtra = []string{"      tls:\n        cert: " + cert + "\n        key: " + key + "\n"}
		return nil
	})
	sc.Step(`^the receiver saw the proxy's client certificate$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range w.receivers[0].requests() {
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
			value = "{ file: " + w.writeFile("otlp/token", secret+"\n") + " }"
		case "an environment variable":
			w.env = map[string]string{"OTLP_TOKEN": secret}
			value = "{ env: OTLP_TOKEN }"
		}
		w.exporterExtra = []string{"      headers:\n        Authorization: " + value + "\n"}
		return nil
	})
	sc.Step(`^every request to the receiver carried that Authorization value$`, func() error {
		w.settle()
		seen := w.receivers[0].requests()
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
		w.settle()
		if strings.Contains(w.logs.String(), "s3cr3t-otlp-token") {
			return failf("the log holds the secret: %s", w.logs.String())
		}
		return nil
	})

	sc.Step(`^an? (gRPC|HTTP) receiver and an exporter that (does not set compression|sets compression to none)$`, func(protocol, sets string) error {
		if protocol == "HTTP" {
			use(receiverSetup{http: true})
		}
		if sets == "sets compression to none" {
			w.exporterExtra = []string{"      compression: none\n"}
		}
		return nil
	})
	sc.Step(`^every request to the receiver is (gzipped|plain)$`, func(encoding string) error {
		w.settle()
		want := map[string]string{"gzipped": "gzip", "plain": ""}[encoding]
		seen := w.receivers[0].requests()
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

	sc.Step(`^an HTTP receiver that answers the first log send with (\d+)(?: with Retry-After (\d+))?$`, func(status int, retryAfter string) error {
		use(receiverSetup{http: true})
		w.receivers[0].answers = []answer{{status: status, retryAfter: retryAfter}}
		w.exportTimeout = 5 * time.Second
		return nil
	})
	logSends := func() int {
		n := 0
		for _, seen := range w.receivers[0].requests() {
			if seen.logs {
				n++
			}
		}
		return n
	}
	sc.Step(`^the record arrives on the second try$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		if got := logSends(); got != 2 {
			return failf("the record was sent %d times, want 2", got)
		}
		return nil
	})
	sc.Step(`^the record is lost, and the proxy logs that 1 was dropped$`, func() error {
		if got := len(w.records()); got != 0 {
			return failf("the receiver got %d records, want none", got)
		}
		if got := logSends(); got != 1 {
			return failf("the record was sent %d times, want 1", got)
		}
		for _, line := range strings.Split(w.logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "dropped=1") &&
				strings.Contains(line, w.receivers[0].endpoint) {
				return nil
			}
		}
		return failf("no warning about the dropped record; the log is %s", w.logs.String())
	})

	sc.Step(`^a gRPC receiver that answers the first log send with (Unavailable|InvalidArgument|ResourceExhausted)( with RetryInfo 1s)?$`, func(code, retryInfo string) error {
		codeOf := map[string]codes.Code{"Unavailable": codes.Unavailable, "InvalidArgument": codes.InvalidArgument, "ResourceExhausted": codes.ResourceExhausted}
		answer := status.New(codeOf[code], "the receiver refuses this export")
		if retryInfo != "" {
			var err error
			if answer, err = answer.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)}); err != nil {
				return err
			}
		}
		w.receivers[0].grpcAnswers = []*status.Status{answer}
		w.exportTimeout = 5 * time.Second
		return nil
	})
	sc.Step(`^an HTTP receiver (over TLS|plain) that offers (HTTP/2|HTTP/1.1|h2c)$`, func(over, offers string) error {
		setup := receiverSetup{http: true, http2: offers == "HTTP/2", h2c: offers == "h2c"}
		if over == "over TLS" {
			setup.tls = &tls.Config{
				Certificates: []tls.Certificate{w.issuer.leaf("localhost", time.Now().Add(24*time.Hour), false)},
				MinVersion:   tls.VersionTLS12,
			}
		}
		use(setup)
		// A send that cannot be read is tried again until its time runs out.
		w.exportTimeout = 300 * time.Millisecond
		return nil
	})
	sc.Step(`^the exporter (does not set h2c|sets h2c to true)$`, func(sets string) error {
		if sets == "sets h2c to true" {
			w.exporterExtra = []string{"      h2c: true\n"}
		}
		return nil
	})
	sc.Step(`^the receiver gets the record$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		return nil
	})
	sc.Step(`^every request to the receiver came over (HTTP/2.0|HTTP/1.1)$`, func(version string) error {
		w.settle()
		seen := w.receivers[0].requests()
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
			"OTEL_EXPORTER_OTLP_ENDPOINT":    "http://" + freePort(),
			"OTEL_EXPORTER_OTLP_HEADERS":     "x-from-env=leaked",
			"OTEL_EXPORTER_OTLP_COMPRESSION": "none",
		} {
			if err := os.Setenv(name, value); err != nil {
				return err
			}
			w.processEnv = append(w.processEnv, name)
		}
		return nil
	})
	sc.Step(`^the receiver gets the record, gzipped, with no header from the environment$`, func() error {
		if got := len(w.records()); got != 1 {
			return failf("the receiver got %d records, want 1", got)
		}
		for _, seen := range w.receivers[0].requests() {
			if seen.encoding != "gzip" || seen.header.Get("X-From-Env") != "" {
				return failf("a request had encoding %q and headers %v", seen.encoding, seen.header)
			}
		}
		return nil
	})
}
