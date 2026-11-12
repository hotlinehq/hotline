# The hotline-proxy program

The proxy from slice 1 runs as one program, `hotline-proxy`, in `src/hotline-proxy/cmd/`. It stays thin: it reads its command line, connects the real network, clock and log to the `proxy` package, starts the proxy and stops it. All decisions below are Peter's, 2026-10-28. The behaviour is written down in `src/hotline-proxy/features/program.feature`.

## What it does

1. **Commands.** `hotline-proxy run --config hotline-proxy.yaml` starts the proxy. `hotline-proxy validate --config hotline-proxy.yaml` only checks the config and the specs it points to, then exits. `hotline-proxy version` prints `hotline-proxy <version> (commit <first 12 characters of the git commit>)`, from what the build passed in, else what Go recorded (Peter, 2026-11-10). A version that already is the commit is shown once. `hotline-proxy version --format json` prints `{"version":"v0.1.0","commit":"0123456789ab"}` (Peter, 2026-11-12). The downloadable program file has the same name ([proxy-binaries.md](proxy-binaries.md)).
2. **Config path.** Given only with `--config`. No default path and no environment variables. Without it the program stops with exit code 2 and says why.
3. **Listen address.** Comes only from `listen.proxy` in the config.
4. **Health checks.** `/livez` and `/readyz` on `listen.admin`, and the `--drain-delay` flag: built later, see [proxy-health-checks.md](proxy-health-checks.md).
5. **Logs.** The proxy's own log lines go to standard error (the program's error output). Every command takes `--format` (Peter, 2026-11-12): `console`, the default, writes plain lines for a person, coloured when standard error is a terminal and `NO_COLOR` is not set; `json` writes one JSON object per line, for log collectors, and makes the console exporter print JSON too ([proxy-console-export.md](proxy-console-export.md)). The Docker image and the Kubernetes sidecar run with `--format json`. `--log-level` (debug, info, warn, error) sets the lowest level logged; the default is info. Help and usage text, and a wrong command line, are always plain text.
6. **Stopping.** On SIGTERM (what Kubernetes sends to stop a program) or SIGINT (Ctrl+C), the proxy stops taking new calls, lets calls in progress finish, and sends every queued record. It takes at most 25 seconds, set with `--shutdown-timeout`, so it ends before Kubernetes' usual 30 second limit. A second signal cuts the stop short.
7. **Exit codes.** 0 after a clean stop. 1 when it cannot start (config refused, trust file refused, port taken), or when a stop ran out of time or was cut short. 2 for a wrong command line.
8. **Trust file.** `--trust-file` names a PEM file (the usual text format for certificates) of extra trusted issuers for vendor certificates, added on top of the system's own. The vendor's certificate is still always checked. The end-to-end tests use it to trust their test vendor.
9. **Fixed settings.** One send may take at most 10 seconds. How often each exporter sends is in its config: `metricInterval` (60s), `logInterval` and `traceInterval` (15s each). Each run gets a new random `service.instance.id`.

## End-to-end tests

- `make e2e` builds the program, runs the scenarios tagged `@e2e` against it as a separate process, and writes the program's coverage to `e2e-cover.out`. `make test` runs them too and adds that coverage to the total.
- Without make: `go test -tags e2e -run TestEndToEnd .` from `src/hotline-proxy`. It builds the program itself. The normal `go test ./...` skips them.
- The tests reach the program only through its command line, output, exit code and the network: a local TLS test vendor at `localhost` and a fake OTLP receiver.
- Only the Go client cases from `research/matrix-clients/go` are in for now (cases 1 and 2). The Node, Java and Rust clients and the TLS cases (4 and 5) are tagged `@later`.
- The "host that does not exist" case looks up `nothing.invalid` through the real DNS resolver. `.invalid` is a name kept by RFC 6761 that never exists.
