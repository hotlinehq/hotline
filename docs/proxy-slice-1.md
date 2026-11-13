# Proxy slice 1: scope and test cases

The proxy is built in small slices, tests first. This page says what slice 1 covers, what waits for later, and which parts of the spec need an answer before building starts. The source of truth is `proxy-spec.md` and `proxy-example/` in the `hotline-strategic` repo.

Slice 1 is built. The test cases are Gherkin feature files (plain-language cases in Given, When, Then form) in `src/hotline-proxy/features/`, run by godog, and the code in `src/hotline-proxy/` makes them pass. Peter answered the 14 open questions on 2026-10-28; the answers are listed at the end of this page.

## What slice 1 covers

- **Forward proxy ingress only.** The app puts the full vendor address in the request line (`PUT https://api.talon.one/...`), with `http` or `https`.
- **TLS origination** (the proxy makes the `https` call to the vendor itself): on by default, off per api with `tlsOrigination: false`.
- **Integration ID** from `Proxy-Authorization: Basic`, the user name part. A missing or unknown ID, or a host outside the integration's apis, uses the reserved `not-passed` settings.
- **Phase timings:** DNS, connect, TLS, first byte and total, the proxy's own time before and after the vendor call, plus the vendor IP and whether the connection was reused.
- **The request record**, with redaction driven by the `x-hotline-redact` marks in our copy of the vendor's OpenAPI spec.
- **OpenTelemetry output** over OTLP: one log record per call and the `hotline.requests` counter, with the standard OpenTelemetry names and `hotline.*` for the rest.
- **The proxy's own errors:** `502` or `504` with a `Proxy-Status` header (RFC 9209, the standard for proxy error answers).
- **Loading the config file** once at start.

The proxy judges nothing. Verdicts, thresholds and SLAs live in the control plane.

## Where the test cases are

| File | What it describes |
|---|---|
| `config.feature` | loading and checking `hotline-proxy.yaml` |
| `spec.feature` | reading route templates, marks and secrets from the vendor spec |
| `ingress.feature` | finding the ingress |
| `integration-id.feature` | reading the integration ID |
| `forward.feature` | the call to the vendor, certificates, the proxy's own errors |
| `timing.feature` | phase timings |
| `redaction.feature` | redacting the vendor address |
| `headers.feature` | choosing headers for the record |
| `record.feature` | building the request record |
| `export.feature` | sending logs and the counter over OTLP |
| `end-to-end.feature` | real clients through the real proxy, tagged `@e2e` |

Run them with `go test ./src/hotline-proxy/`. Scenarios tagged `@later` are skipped for now, and those tagged `@e2e` run against the built program with `make e2e` (see [proxy-program.md](proxy-program.md)). The proxy as a whole is the system under test: scenarios drive it the way an app does, with fake vendors and a fake OTLP receiver at its edges (see the `hotline-go-proxy` skill).

## End-to-end checks

The end-to-end tests reuse the client matrix from the research folder: `research/matrix-clients/go`, `research/matrix-clients/node` and the prototype proxy in `research/hotline-matrix-test`. The case numbers are those of the matrix in `egress-proxy-options.md`. Only cases 1, 2, 4 and 5 put a full address in the request line, so only they belong to slice 1. The rest arrive with the Host header ingress.

Unlike the research runs, these tests never call the real Stripe. They use a local TLS test vendor whose certificate issuer is trusted through `SSL_CERT_FILE` (an environment variable Go reads on Linux to find trusted issuers), and a local fake OTLP receiver. The Java and Rust clients from the first milestone come in a later slice.

## What came after slice 1

Checked against `main` on 2026-10-31. Most of what slice 1 left for later is now built:

- **Built:** the Host header ingress with HTTP/2 from apps ([proxy-host-header.md](proxy-host-header.md)), the vendor URL in path ingress ([proxy-url-in-path.md](proxy-url-in-path.md)), the CONNECT ingress ([proxy-connect.md](proxy-connect.md)), the transparent redirect ingress ([proxy-transparent-redirect.md](proxy-transparent-redirect.md)), body checks against the spec and the 1 MiB body limit ([proxy-spec-checks.md](proxy-spec-checks.md)), body and record signatures with the `fingerprintKey` ([proxy-signatures.md](proxy-signatures.md)), config reload ([proxy-config-reload.md](proxy-config-reload.md)), the admin port health checks ([proxy-health-checks.md](proxy-health-checks.md)), the proxy's own telemetry ([proxy-telemetry.md](proxy-telemetry.md)), TLS from apps ([proxy-tls-from-apps.md](proxy-tls-from-apps.md)), streaming replies and WebSockets passing through, trace context on records ([proxy-trace-context.md](proxy-trace-context.md)), and the `hotline-proxy` program with `validate` and the `@e2e` scenarios ([proxy-program.md](proxy-program.md)).
- **Built since:** each call's timings as a trace ([proxy-traces.md](proxy-traces.md)).
- **Still later:** the load test with **k6** (Grafana's open source load testing tool, scripted in JavaScript; Peter, 2026-10-27) and with it `hotline.schema.skipped=overloaded`, and the container image.

## Answers to the open questions

Peter answered these on 2026-10-28. Answers 1, 12, 13 and 14 were overtaken by later slices; the note after each says what holds now. `proxy-spec.md` in `hotline-strategic` is the source of truth.

1. **Other requests on the proxy port.** A path, a Host header request or `CONNECT` gets `501` with `Proxy-Status: hotline; error=http_request_error`. A full address the proxy can't use, such as `ftp://` or one with no host, gets `400`. *Now:* the Host header, vendor URL in path and CONNECT requests are ingresses of their own. `CONNECT` still gets `501` on a listener without the `connect` part. Every refused request gets a minimal record with `error.type=http_request_error`.
2. **Broken `Proxy-Authorization`.** Every broken form counts as no ID, so the record says `not-passed`.
3. **A path with no route in the spec.** `url.full` and `url.template` are left out.
4. **Turning `http` into `https`.** The app's own port stays: `http://api.x.com:8080` goes out as `https` on 8080.
5. **`not-passed` and `https`.** An `https` call stays `https`. *Since 2026-11-01:* a `not-passed` call, or a call to a host outside the integration's apis, also turns `http` into `https`, unless the `not-passed` entry sets `tlsOrigination: false` (Peter, 2026-11-01).
6. **Forwarding headers.** The proxy adds none.
7. **Config version.** ~~A `version` field in the config file, written by whoever changes it.~~ Replaced (Peter, 2026-11-01): records carry the config file's name and a fingerprint of its files; the config has no `version` field.
8. **Strict config.** Lenient: unknown fields and an `apis` list on `not-passed` are accepted, and each gets a warning log line.
9. **Matching routes.** A trailing slash is ignored. The path of the first `servers` address goes in front of every route.
10. **Timing definitions.** Each timing is the length of its own phase, except `first_byte`, which counts from when the call reached the proxy. The proxy's own time is split in two: `hotline.timing.proxy_request_us` (before the vendor call starts) and `hotline.timing.proxy_response_us` (after the vendor's reply ends). Together they are the total minus the vendor's part.
11. **Empty query values.** `customer=` stays as it is.
12. **Fields not built yet.** The schema and fingerprint fields are left out. *Now:* body checks fill the `hotline.schema.*` fields, and bodies and records are signed instead of fingerprinted (see [proxy-signatures.md](proxy-signatures.md)).
13. **Dropped records.** A warning log line, at most once a minute per destination, and always one on the final send at shutdown. Each exporter's queue holds 10 000 records unless `queueSize` says otherwise. *Now:* dropped records are also counted in the proxy's own telemetry, as `hotline.exporter.dropped` with `hotline.drop.reason=queue_full`.
14. **TLS from apps to the proxy.** A later slice. *Now:* built (see [proxy-tls-from-apps.md](proxy-tls-from-apps.md)).

One more answer came from the review of the pull request (Peter, 2026-10-28): `vendorTimeout` also limits how long a reply may go quiet between its parts. A vendor that stays silent longer is cut off, and the record says `http_response_incomplete`. A WebSocket may stay quiet as long as it likes.
