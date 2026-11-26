Feature: The code still follows the decision log
  Each file in docs/decisions ends with a Tripwires section. Its
  Confirmation line says how to check that the code still follows that
  decision, and each scenario here is one such line, named after its file,
  so a pull request that goes back on a decision fails here. Some scenarios
  repeat what another feature file tests, on purpose: this file holds every
  check in one place (Peter, 2026-11-25, tripwires decisions 2 and 3).

  "Proxy code" is every Go file of this module but its tests, read without
  its comments, so a word in a comment never counts. "The proxy code in X"
  and "the package X" start from this module's folder, src/hotline-proxy;
  "the file X" and "the workflow X" from the repository's root.

  Rule: Rules about the proxy's source

    Scenario: tripwires. Every decision file has a Confirmation line and a scenario here
      Then every file in docs/decisions ends with Tripwires, a Confirmation and a Revisit if line
      And every file in docs/decisions has a scenario in this feature named after it

    Scenario: aws-sqs. The proxy never reads the Authorization header
      Then the text "\"Authorization\"" is in proxy code only in internal/forward/forward.go and internal/spec/compile.go

    Scenario: capture. Only a capture listener reads the capture headers
      Then the text "captureHeader" is in proxy code only in capture.go
      And the text "captureProcess(" is in proxy code only in capture.go

    Scenario: internal-guard. Every connection to a vendor goes through the guard
      The guarded dialer in dial.go opens every connection to a vendor. The
      only other dialers are the one the program hands to it, and the check
      of the proxy's own socket file.
      Then the text "net.Dial" is in proxy code only in cmd/main.go and capture.go

    Scenario: otlp-encoder. No gRPC
      Then no proxy code imports "google.golang.org/grpc"

    Scenario: tls-from-apps. TLS 1.2 at least, and no chosen ciphers
      Then no proxy code holds "CipherSuites"
      And no proxy code holds "tls.VersionTLS10"
      And no proxy code holds "tls.VersionTLS11"

    Scenario: probe. Probes run on 2 workers
      Then the proxy code in probe.go matches "probeWorkers = 2"

    Scenario: connect. Host certificates last 24 hours and stay in memory
      Then the proxy code in connect.go matches "leafLife\s+= 24 \* time.Hour"
      And the proxy code in connect.go does not import "os"

    Scenario: config-reload. One check every 5 seconds, and SIGHUP
      Then the proxy code in cmd/main.go matches "checkEvery\s+= 5 \* time.Second"
      And the proxy code in cmd/main.go holds "syscall.SIGHUP"

    Scenario: file-export. Lines are written with jsontext
      Then the proxy code in internal/export/line.go holds "jsontext.NewEncoder"

    Scenario: forwarder. Our own forwarder, not Go's reverse proxy
      Then no proxy code holds "httputil.ReverseProxy"
      And the proxy code in handler.go holds "h.forward.Forward("

    Scenario: glossary. One word per thing
      Then the file GLOSSARY.md exists
      And the package internal/export declares no type named Sink
      And the package internal/config declares no type named Intercept

    Scenario: proxy-telemetry. No request counter to exporters
      Then no proxy code holds "hotline.requests"

    Scenario: spec-checks. A content's place has its own type, and reading stops 64 levels deep
      Then the proxy code in internal/spec/schemacheck.go holds "type ContentIndex int"
      And the proxy code in internal/spec/read/doc.go matches "maxDepth\s+= 64"

    Scenario: websockets. Sessions and messages have records of their own
      Then the proxy code in internal/export/readline.go holds "hotline.websocket.session"
      And the proxy code in internal/export/readline.go holds "hotline.websocket.message"

    Scenario: program. No context kept in a struct, and long-lived signals close once
      Then the linter containedctx is on in .golangci.yaml
      And the proxy code in onetimeevent.go holds "oneTimeEvent"

  Rule: Rules about the repository's other files

    Scenario: fuzzing. Fuzz workers keep their output, and the fuzz run is nightly
      Then every package with a fuzz test calls fuzzlog.Capture()
      And the file .github/workflows/fuzz.yml holds "0 1 * * *"

    Scenario: lint. Stale skips fail lint
      Then the linter nolintlint is on in .golangci.yaml

    Scenario: streamed-responses. The Anthropic spec gives its stream an itemSchema
      Then the file specs/anthropic/messages/2023-06-01.openapi.yaml holds "itemSchema"

    Scenario: load-runner. The load test starts only on main or by hand
      Then the workflow load-test.yml starts only on a push to main or by hand
      And the workflow load-test.yml has no concurrency group
      And only the workflow load-test.yml names the runner "hotline-load"
      And the file .github/workflows/load-test.yml holds "=~ ^[0-9a-f]{40}$ ]]"
      And the file .github/workflows/load-test.yml holds "HEAD:src/hotline-proxy"
      And the file Makefile holds "small-json 20000 $(SSM_SPEC)"

    Scenario: floci-example. Floci is pinned and its workflow starts only on main or by hand
      Then the file examples/aws-shop/floci/up.sh matches "floci/floci:\d+\.\d+\.\d+"
      And the workflow aws-shop-floci.yml starts only on a push to main or by hand

    Scenario: tests-in-ci. make test runs nothing under the race detector
      Then the Makefile's test target does not hold "-race"

    Scenario: sqs-example. The SQS spec is made from AWS's model
      Then the file tools/aws-openapi/smithy/convert_test.go holds "made the committed SQS spec from the committed SQS model"

    Scenario: slice-plan. Every built item points to a doc that exists
      Then every link in docs/decisions/slice-plan.md leads to a file that exists

    Scenario: transparent. The real firewall redirect is tested where it can be
      Then the file src/hotline-proxy/features/end-to-end-transparent.feature holds "a firewall redirects its connection to the listener"

  Rule: Behaviour of the proxy

    Scenario Outline: listeners and slice-1. Old sections and unknown fields only give warnings
      Given a config file where <problem>
      When the proxy loads it
      Then the proxy starts and logs a warning naming <name>

      Examples:
        | problem                                        | name          |
        | the config still has a listen section          | listen        |
        | the config still has an issuers section        | issuers       |
        | a field name has a typo, such as tlsOrigintion | tlsOrigintion |

    Scenario: slice-1. No forwarding headers are added
      Given an app call
      When the proxy forwards it
      Then the vendor gets no X-Forwarded-For or Via header

    Scenario: host-header. The older Upgrade: h2c handshake is ignored
      When an app sends "GET /v2/x" with "Host: api.talon.one" and "Upgrade: h2c"
      Then the connection stays HTTP/1.1
      And the vendor gets the call

    Scenario: transparent-redirect. A redirected call is a Host header call
      When an app sends "PUT /v2/customer_sessions/sess-1" with "Host: api.talon.one"
      Then the record says hotline.ingress host_header

    Scenario: url-in-path. A squashed slash gets 400 and a minimal record
      When an app sends "GET /https:/api.talon.one/v2/x" with "Host: localhost:8080"
      Then the proxy answers 400 with "Proxy-Status: hotline; error=http_request_error"
      And the record is minimal with hotline.ingress url_in_path, http.response.status_code 400 and error.type http_request_error

    Scenario: trace-context. The app's trace is in the record's own trace fields
      Given the shop spec
      When the app sends GET /orders with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the record's trace_id is 4bf92f3577b34da6a3ce929d0e0e4736 and its flags are 01

    Scenario: ledger. Every field of a record is covered by its hash
      The list of covered fields is closed: a field the proxy starts to send
      fails here until the hash covers it (Peter, 2026-11-25).
      Given the shop spec
      And the proxy's clock is a test clock, so a wait of minutes passes at once
      When the app sends GET /orders
      And 10 seconds pass
      Then every LogRecord field the receiver got, on the call record and the batch record, is one the hash or signature covers, in a scope named hotline-proxy
      And the hash checks out when rebuilt from the record alone

    Scenario: ledger. verify passes on the folder a file exporter wrote
      Given the proxy's clock is a test clock, so a wait of minutes passes at once
      And a file exporter writing to the folder calls
      When the app sends GET /orders 5 times
      And 30 seconds pass
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0

    @e2e
    Scenario: health-checks. An exporter that is down keeps the proxy ready
      Given a fake OTLP receiver that refuses connections
      And the hotline-proxy program runs with integration test-shop for the test vendor
      Then GET /readyz on the admin port answers 200

    @e2e
    Scenario: profile-pages. Off by default, the admin port has no profile pages
      Given a fake OTLP receiver
      And the hotline-proxy program runs with integration test-shop for the test vendor
      Then GET /debug/pprof/ on the admin port answers 404

    @e2e
    Scenario: console-export. Calls on standard output, logs on standard error
      Given a fake OTLP receiver
      And the hotline-proxy program runs with a console exporter and integration test-shop for the test vendor
      When the Go client calls the test vendor
      And the program gets SIGTERM
      Then it exits with code 0
      And standard output shows the call as two plain lines
      And standard error has plain lines, from the start to the stop
