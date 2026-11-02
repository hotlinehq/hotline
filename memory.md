# Project memory

Decisions already made, so a new session does not have to dig through old threads and pull requests. Keep each entry short and point to where the detail lives. Add to it when Peter decides something new.

## Where the rules live

- How to test and the TDD loop: [CLAUDE.md](CLAUDE.md).
- How to write, test and review proxy code in Go: the [`hotline-go-proxy` skill](.claude/skills/hotline-go-proxy/SKILL.md). It covers the system under test, testing only through the proxy's ways in and out, and no test-only code.
- Every number in a pull request description, commit message or doc is well defined: say what was measured, over what (whole process or proxy only, per call or in total, allocated over time or held at once), how, and its unit (Peter, 2026-11-02).
- Every `x-hotline-` mark in a vendor spec, with its use and an example: [docs/spec-marks.md](docs/spec-marks.md). A new mark is added only when a real example needs it, and listed there in the same pull request (Peter, 2026-11-01).
- The proxy's spec and examples: `proxy-spec.md` and `proxy-example/` in the `hotline-strategic` repo.
- What the proxy sends over OpenTelemetry, field by field: [docs/otel-export.md](docs/otel-export.md). Its own numbers, for the `telemetry` destination: [docs/proxy-telemetry.md](docs/proxy-telemetry.md).

## Code layout

- Examples of apps using Hotline live in `examples/`, each its own Go module listed in `go.work`. The first was `examples/aws-sqs-invoices` (AWS SDK for Go v2, invoice PDF jobs over SQS; [docs/proxy-sqs-example.md](docs/proxy-sqs-example.md), Peter, 2026-10-30), renamed `examples/aws-shop` on 2026-11-01: one program, four apps (`checkout`, `invoices`, `mailer`, `refunds`), each its own integration, and `run-shop.sh` running the whole story. It uses only AWS resources that already exist and never creates or removes any. Detail: [docs/proxy-shop-example.md](docs/proxy-shop-example.md). `run-shop.sh` pays a new order each run (`A-` plus the date and time; any `A-` number is a sample order with A-1001's lines), since an order is invoiced only once, and retries the invoices step while the topic passes the job on (Peter, 2026-11-01).
- The SQS examples and tests use Peter's own AWS account, `192325781350`, in `eu-west-1`, so the example runs against real AWS as is. The recorded signed call in `testdata/aws/sqs-sendmessage-signed.http` keeps `123456789012`, since changing its body would break its signature (Peter, 2026-10-30).
- Stable vendor specs live in `specs/<vendor>/<api>/<vendor version>.openapi.yaml` (for example `specs/aws/sqs/2012-11-05.openapi.yaml`), the vendor's api only. Overlays are test or example specific and live with them, never in `specs/`. Rules: [specs/README.md](specs/README.md) (Peter, 2026-10-30).
- The proxy is its own Go module, `hotline-proxy`, in `src/hotline-proxy/`: the `proxy` package at the top, then `cmd/` (the program), `features/`, `internal/` and `testdata/`. The metrics code stays in module `hotline`, in `src/hotline/metrics/`. Both are listed in `go.work` (Peter, 2026-10-30).

## Testing, in one line each

- Proxy behaviour: Gherkin feature files in `src/hotline-proxy/features/`, run by godog. Everything else: Ginkgo and Gomega.
- The whole proxy is the system under test (the thing a test drives and checks), with fake vendors and a fake OTLP receiver at its edges.
- Never change production code just so a test can reach it.
- Run `make lint` before every push; CI lint finds what tests miss (Peter, 2026-11-02). It needs golangci-lint v2.13 built with the repo's Go: `GOTOOLCHAIN=go1.27.1 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.0`.

## Slice plan

The proxy is built in small slices, tests first.

1. **Slice 1: done** (pull request #11). Forward proxy ingress, TLS to the vendor, integration ID, phase timings, the request record with redaction, OTLP output, the proxy's own errors, config loaded once. Scope and detail: [docs/proxy-slice-1.md](docs/proxy-slice-1.md).
2. **The `hotline-proxy` program: built** (commands, flags, stopping, exit codes, `@e2e` tests). Detail: [docs/proxy-program.md](docs/proxy-program.md).
3. **TLS from apps to the proxy: built** (`listen.tls`, renewal without a restart, Go cases 4 and 5 end to end). Detail: [docs/proxy-tls-from-apps.md](docs/proxy-tls-from-apps.md).
4. Still to come, roughly in this order:
   - the health checks `/livez` and `/readyz` on the admin port, with a short wait (`--drain-delay`, 5 seconds) before the proxy port closes on stop: built. Detail: [docs/proxy-health-checks.md](docs/proxy-health-checks.md);
   - the Node, Java and Rust clients in the end-to-end tests;
   - the vendor URL in path ingress: built, with refused calls recorded on every ingress. Detail: [docs/proxy-url-in-path.md](docs/proxy-url-in-path.md);
   - the guard against calls to internal addresses: built. Detail: [docs/proxy-internal-guard.md](docs/proxy-internal-guard.md);
   - the Host header ingress, with HTTP/2 from apps: built. Detail: [docs/proxy-host-header.md](docs/proxy-host-header.md);
   - each vendor api named once in the config, shared by integrations: built. Detail: [docs/proxy-spec-checks.md](docs/proxy-spec-checks.md), decisions 10 and 11;
   - checks of every part of a call against the spec (path, query, headers, cookies, body; and the reply's status, headers and body). Built. Detail: [docs/proxy-spec-checks.md](docs/proxy-spec-checks.md);
   - loading a large spec cheaply (Peter, 2026-11-01): a schema that is only a `$ref` reuses the shared shape's check, and a schema needing nothing else from the spec is built apart, once per text; SSM loads in 0.5 s and 300 MB instead of 1.3 s and 950 MB. Built. Detail: [docs/proxy-spec-checks.md](docs/proxy-spec-checks.md), details settled while building;
   - the transparent redirect ingress: built, as a Host header call with no proxy code change. Detail: [docs/proxy-transparent-redirect.md](docs/proxy-transparent-redirect.md);
   - AWS, starting with SQS: signed calls stay valid, actions as routes named by a header. Built 2026-10-30. Detail: [docs/proxy-aws.md](docs/proxy-aws.md);
   - the SQS invoice example: a string holding JSON (`contentMediaType` with `contentSchema`) is checked, AWS's JSON content type read as JSON where the spec marks it `x-hotline-body: json` (the proxy has nothing AWS-specific; Peter, 2026-10-30), the stable SQS spec made from AWS's Smithy model by `tools/aws-openapi`, one overlay per queue, and end-to-end tests with the example app. Built. Detail: [docs/proxy-sqs-example.md](docs/proxy-sqs-example.md);
   - XML bodies and S3, in one step driven by S3 in the invoice example (Peter, 2026-10-31). XML bodies for any vendor, read by the spec's shape and OpenAPI `xml` notes, then checked like JSON; `x-hotline-body` takes `json` or `xml`; a DOCTYPE is refused; namespaces are ignored. Routes picked by a query word after `#` (`/{Key+}#?tagging`), and `{name+}` takes the rest of the path, last part only. The stable S3 spec from AWS's Smithy model, with the bucket in the host name and `x-id` kept only where routes would clash. The example stores, fetches, lists and cleans up invoice PDFs in S3. Built. Detail: [docs/proxy-xml-bodies.md](docs/proxy-xml-bodies.md);
   - the shop example, step 1 (Peter, 2026-11-01): stable specs for DynamoDB, EventBridge, Kinesis, Secrets Manager, SSM, KMS, CloudWatch Logs and Step Functions; one api per AWS resource with its own overlay; base64 text holding JSON is decoded before its check (`contentEncoding: base64`, rule `base64`); AWS's Java patterns rewritten for Go by `tools/aws-openapi`, or kept as `x-aws-pattern` and not checked. Built. Detail: [docs/proxy-shop-example.md](docs/proxy-shop-example.md);
   - the shop example, step 3 (Peter, 2026-11-01): SNS and STS in AWS's query form (action in the form field `Action`), with `x-hotline-action-field` picking routes by a top-level body field once the body is whole, the reply check waiting for it, and the kept body grown from 1 MiB to 2 MiB; Lambda and SES v2 on AWS's REST and JSON form. Checkout publishes the unchanged invoice job on the `order-paid` topic; the invoices worker takes on the `invoice-worker` role, which covers all its calls, and gets the payment text from the `payment-qr` function; the mailer sends with SES from `MAIL_FROM`. No mark for dotted list names until a real example needs one. Built. Detail: [docs/proxy-aws-query.md](docs/proxy-aws-query.md), [docs/proxy-shop-example.md](docs/proxy-shop-example.md);
   - the CONNECT ingress, with interception: the proxy poses as the vendor inside the tunnel with certificates signed by the customer's issuer. Built. Detail: [docs/proxy-connect.md](docs/proxy-connect.md);
   - OTLP over HTTP, and TLS, login headers and gzip for both gRPC and HTTP: built. Detail: [docs/proxy-otlp-protocols.md](docs/proxy-otlp-protocols.md);
   - a level on every record (INFO, WARN for a spec problem or a 4xx, ERROR for a 5xx or a proxy failure), no body text, and resource labels per exporter (`resource`, values written out or from `env`/`file`; `service.name` and `service.instance.id` may change, `service.version` may not). Built 2026-10-30, for Grafana Cloud. Detail: [docs/otel-export.md](docs/otel-export.md);
   - a signature on every full record: HMAC-SHA256 with the integration's `fingerprintKey` over every attribute sorted by name plus the timestamp, no list of names (`hotline.signature`, `hotline.signature_key_id`), and each body signed on its own as it passes (`hotline.request.body.signature`, `hotline.response.body.signature`), covered by the record's signature (Peter, 2026-10-31; replaces the RFC 9421 form of 2026-10-30). Detail: [docs/proxy-signatures.md](docs/proxy-signatures.md);
   - body fields in the record: a spec marks a body field `x-hotline-record: <attribute name>`, and the proxy copies its plain value (text cut at 256, number, true or false; never inside a list) into full records, signed, never into the counter. Names like the proxy's own are refused. The same mark on a call header (never a login header) copies the header as text. The SQS spec marks `QueueUrl` as `messaging.destination.name`, the full address, and lists `X-Amz-Target` on every action as `messaging.operation.name` (Peter, 2026-10-31). Built. Detail: [docs/proxy-body-fields.md](docs/proxy-body-fields.md);
   - overlays on an api's spec: OpenAPI Overlay 1.1 only, on the api only, applied at load in order, a target matching nothing is a warning, library `speakeasy-api/openapi/overlay`, plus the `hotline-proxy overlay` command that makes one from two specs (Peter, 2026-10-30). Built; needed by the SQS example. Detail: [docs/proxy-overlays.md](docs/proxy-overlays.md);
   - timings are whole microseconds, each name ending in `_us`, such as `hotline.timing.total_us` (Peter, 2026-10-31); since the trace, `total_us` is the only one left on the record. Detail: [docs/otel-export.md](docs/otel-export.md);
   - config reload without a restart: built. The files are checked every 5 seconds and SIGHUP reloads at once; certificates use the same check; listen addresses and exporters need a restart; `hotline.config.version` is the config file's name, `+` and a 12 character fingerprint. Detail: [docs/proxy-config-reload.md](docs/proxy-config-reload.md);
   - the proxy's own telemetry: metrics to the `telemetry` destination only, never split by integration, vendor, host, URL or route. Built. Detail: [docs/proxy-telemetry.md](docs/proxy-telemetry.md);
   - each call's timings as a trace, not number attributes (Peter, 2026-10-31): one trace per call to every exporter, no sampling; spans `hotline call` (from the app TLS handshake on a connection's first call), `app tls handshake`, `routing`, `vendor call` with `dns lookup`, `tcp connect`, `vendor tls handshake`, `send call`, `wait for vendor`, `receive reply`, then `reply to app` (failed when the app hangs up), and `request spec check` and `reply spec check`, each run alongside the call as soon as its side is whole (error when the spec is broken, ok when it passed). Only breakdowns fail spans, never the vendor's status. The record has `hotline.schema.status` valid, invalid or skipped. No data on both trace and record unless there is a good reason: the record keeps only `hotline.timing.total_us`; `hotline.connection.reused` and `dns.question.name` moved to spans. The trace is signed, in `hotline.trace.signature` on the record. Built (pull request #54, follow-up after it). Detail: [docs/proxy-traces.md](docs/proxy-traces.md);
   - the Docker image (Peter, 2026-11-01): Google's distroless static base, a static build with Go's own name lookup, `linux/amd64` and `linux/arm64`, no vendor specs in the image (they come with the config; Peter, 2026-11-02), `GOMEMLIMIT` set by hand in the examples, no health check command, releases are git tags (`v0.1.0` first), published to `ghcr.io/petercipov/hotline-proxy` with an SBOM, provenance and a cosign keyless signature. Built. A weekly clean-up keeps releases and `edge`, keeps the 5 newest `sha-` builds and deletes older ones (Peter, 2026-11-02). Detail: [docs/proxy-docker-image.md](docs/proxy-docker-image.md);
   - the load test, with k6. Spike and open questions: `load-test/requirements.md` in the project files (2026-11-02). Its first finding is fixed: the proxy copies bodies through 32 KB buffers borrowed from a shared pool, not a new buffer per call; `features/memory.feature` keeps it so (Peter, 2026-11-02).

## Slice 1 decisions (Peter, 2026-10-28)

Full wording is at the end of [docs/proxy-slice-1.md](docs/proxy-slice-1.md).

1. Other requests on the proxy port (a plain path, a Host header request, `CONNECT`) get `501`. `CONNECT` is an ingress since 2026-10-29, when `listen.connect` is set. A full address the proxy can't use gets `400`.
2. A broken `Proxy-Authorization` header counts as no integration ID: `not-passed`.
3. A path with no route in the spec: `url.full` and `url.template` are left out.
4. Turning `http` into `https` keeps the app's port.
5. `not-passed` calls that use `https` stay `https`. Since 2026-11-01 calls outside the config (no known ID, or a host outside the integration's apis) also turn `http` into `https`; the `not-passed` entry can set `tlsOrigination: false` (Peter, 2026-11-01, after the shop example called Secrets Manager on port 80).
6. The proxy adds no forwarding headers.
7. The config file has no `version` field (Peter, 2026-11-01): records carry the config file's name and a fingerprint instead. An old `version` field only gives a warning.
8. Config checks are lenient: unknown fields are accepted, with a warning log line each.
9. Route matching ignores a trailing slash, and puts the path of the first `servers` address in front of every route.
10. Each timing is its own phase, except `first_byte`, counted from arrival. The proxy's own time is split into `hotline.timing.proxy_request_us` and `hotline.timing.proxy_response_us`.
11. Empty query values like `customer=` stay as they are.
12. Fields not built yet (schema, fingerprints) are left out of the record.
13. Dropped records: a warning at most once a minute per destination, plus one at shutdown. Queues hold 10 000 records unless `queueSize` says otherwise. Each exporter sends counts every 60s (`metricInterval`), records and spans every 15s (`logInterval`, `traceInterval`), or sooner once 512 wait (Peter, 2026-11-01): a data point a minute fits Grafana Cloud's standard pricing.
14. TLS from apps to the proxy waits for a later slice.

Also from the review of pull request #11: `vendorTimeout` also limits how long a reply may go silent between its parts. A vendor silent for longer is cut off, recorded as `http_response_incomplete`. WebSockets are exempt.

## Program decisions (Peter, 2026-10-28)

Full list in [docs/proxy-program.md](docs/proxy-program.md).

1. Commands `run`, `validate` and `version`. The config comes only from `--config`, with no default.
2. Own logs: JSON, one object per line, on standard error, with `--log-level`.
3. Stopping: up to 25 seconds (`--shutdown-timeout`); a second signal cuts it short.
4. Exit codes: 0 clean, 1 any failure (also a stop that ran out of time), 2 wrong command line.
5. `--trust-file` adds trusted issuers on top of the system's own, so the end-to-end tests can trust their test vendor on any computer. This changed the spec's vendor certificate rule.
6. End-to-end tests use only the Go client for now. They run with `make e2e`, and `make test` adds their coverage to the 100% check.

## TLS from apps decisions (Peter, 2026-10-28)

Full wording in [docs/proxy-tls-from-apps.md](docs/proxy-tls-from-apps.md).

1. With `listen.tls` set, the proxy port accepts only TLS.
2. Certificate and key are PEM files; on Kubernetes a Secret, usually from cert-manager.
3. Hotline builds nothing for apps to trust the proxy and never makes its own certificate; customers use their own issuer.
4. The files are re-read when they change; a broken new pair keeps the old one. Since 2026-10-31 through the shared config reload (file check every 5 seconds, or SIGHUP).
5. Client certificates (mutual TLS) come later, with caller authentication.
6. TLS 1.2 and 1.3 only, Go's default ciphers, nothing to configure.
7. Only HTTP/1.1 over TLS for now; HTTP/2 comes with the Host header ingress.
8. No record field for it; the proxy's own health data says it once. Changed 2026-10-29: the TLS setup time is recorded as `hotline.timing.app_tls_us`.
9. A missing, broken, mismatched or expired certificate stops the proxy at start. A failed app TLS setup is a debug log line and, later, a count in health data.
10. The warning before the proxy's own certificate expires comes with the health data.

## Vendor URL in path decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-url-in-path.md](docs/proxy-url-in-path.md).

1. Two slices: vendor URL in path first, over HTTP/1.1; then the Host header ingress with HTTP/2 from apps.
2. Only the clean `/http://` or `/https://` form. A squashed slash or an encoded address gets `400`.
3. The vendor gets its own host (and port) from the path in `Host`, never the proxy's.
4. Every request the proxy reads and refuses (`400`, `501`) still gets a minimal record and is counted, on every ingress. Plain HTTP on a TLS port stays without a record.
5. Every record of a call that reached a vendor holds the vendor IP the proxy connected to (`network.peer.address`), minimal records too. Every record also holds the app's IP, from its connection, as `client.address` (Peter, 2026-10-31).
6. No loop guard in this slice; the loop question moves to the Host header slice.

## Internal address guard decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-internal-guard.md](docs/proxy-internal-guard.md).

1. Guard against server side request forgery: after DNS, the proxy refuses to connect to internal IPs (loopback, link-local with the cloud metadata address, private, shared, unspecified, multicast), on every ingress.
2. An api can set `allowInternalIPs: true`; so can the `not-passed` entry. Calls without an ID still reach public hosts.
3. Its own slice, before the Host header ingress. It also stops calls to the proxy itself, so there is no separate loop check.

## Earlier decisions

- Say "ingress", never "door", for the ways apps reach the proxy (Peter, 2026-10-29).
- The load test uses k6 (Peter, 2026-10-27).

## Host header ingress decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-host-header.md](docs/proxy-host-header.md).

1. Calls without a known integration (`not-passed`) go out over `https`, since a Host header call names no scheme.
2. Plain HTTP/2 (h2c) by prior knowledge only; no `Upgrade: h2c` handshake.
3. Guarded from the start by the internal address guard; no separate loop check.
4. No HTTP/3 for now; added later only if a real app needs it.

## Spec checks and shared apis decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-spec-checks.md](docs/proxy-spec-checks.md).

1. Every part of a call is checked against the spec, not only the body: path, query, headers, cookies and body; in the reply, status, headers and body.
2. Unknown query values are reported; unknown headers are ignored; extra body fields follow `additionalProperties`.
3. Each result is structured: `part`, `place`, `rule`, `expected`. Never a value from the call.
4. A content type the spec doesn't list, or a body where none is expected, is reported.
5. A reply status the spec doesn't list (no `default`) is reported, and that reply is not checked further.
6. At most 20 results per side, plus `errors_total` when more.
7. Common formats are checked (`date-time`, `date`, `email`, `uuid`, `uri`, `ipv4`, `ipv6`, `int32`, `int64`).
8. The login key from the security section must be present; its value is never read.
9. Only OpenAPI 3.1; `$ref` within the same file; OpenAPI query styles; Stripe's bracket form bodies.
10. A broken spec is a config problem: refused at start, by `validate` and on reload.
11. One spec is read once and shared by every integration that uses it.
12. The config names each vendor api once in a top-level `apis` section; integrations name the apis they use, and may change only `vendorTimeout`. `not-passed` keeps its settings on itself.

## Transparent redirect decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-transparent-redirect.md](docs/proxy-transparent-redirect.md).

1. The proxy doesn't care how the cluster redirects; the setup guide covers name lookup (`hostAliases`) and network rules in the pod; the tests fake name lookup.
2. The app adds `Proxy-Authorization` itself as a default header; no new way to pass the integration ID.
3. No separate port or record value: a redirected call is a Host header call, `hotline.ingress=host_header`.
4. The port is always kept, port 80 too (`Host: api.x.com:80` goes to `https` on port 80); no port means 443.

## CONNECT ingress decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-connect.md](docs/proxy-connect.md). Why: a proxy deployed apart from the apps needs the hop to it encrypted, with no change in the app.

1. The issuer comes from the customer: `listen.connect: { cert, key }`, re-read on change (since 2026-10-31 through the shared config reload). Without it, `CONNECT` gets `501`.
2. Hotline builds nothing for apps to trust the issuer; the setup guide shows how per language.
3. Every host is intercepted; pinned hosts go in `NO_PROXY`. No blind tunnel.
4. The integration ID comes from `Proxy-Authorization` on the `CONNECT`, for the whole tunnel; one inside is ignored. `CONNECT` works on plain and TLS ports.
5. Host certificates: made on first use, memory only, 24 hours.
6. HTTP/2 and HTTP/1.1 inside the tunnel. `CONNECT` over HTTP/2 works too: the tunnel is that `CONNECT`'s own stream, so many tunnels share one connection (Peter, 2026-10-30).
7. Calls go to the `CONNECT` host and port; another host gets `421`.
8. One record per call, `hotline.ingress=connect`; `hotline.timing.app_tls_us` on the first call of each connection; an app that doesn't trust the issuer gets a minimal record with `tls_untrusted_by_app`.
9. `hotline.timing.app_tls_us` also on TLS from apps (changes TLS from apps decision 8).
10. The issuer is made only for Hotline; files that are not an issuer stop the start; over a year gets a warning.
11. Plain HTTP inside a tunnel is allowed, handled like a forward proxy `http` call.

Found while building: Go's client offers HTTP/2 to an `https` proxy address and then speaks HTTP/1.1, so the proxy sets up TLS with apps itself and picks the HTTP version from the app's first bytes.

## AWS and SQS decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-aws.md](docs/proxy-aws.md).

1. Signed calls stay valid: the proxy forwards `Host`, the path, the body, `Content-Length` and the app's headers unchanged; a rule with a test.
2. The integration ID stays in `Proxy-Authorization`. The proxy never reads `Authorization` (security). No port per integration.
3. Each AWS action is its own route in a spec we generate: `/#X-Amz-Target=AmazonSQS.SendMessage`.
4. The action header is named in our spec (`x-hotline-action-header`), per api, never per integration or in the proxy's code.
5. The test uses a signed request fixture made once; Hotline never depends on the AWS SDK.
6. AWS headers to record are marked in our SQS spec; the default header list is unchanged. No patterns in `hosts`.
7. The setup guide recommends the forward proxy for AWS, with the app's HTTP client turning `https` into `http` after signing, says vendor URL in path can't work for signed hosts, and warns about `NO_PROXY` and VPC endpoints.

## SQS example decisions (Peter, 2026-10-30)

Full wording in [docs/proxy-sqs-example.md](docs/proxy-sqs-example.md).

1. The example: a shop asks for an invoice PDF by putting a job on SQS; a worker renders it. Job fields: `schemaVersion`, `eventId` (not job ID), `orderId`, `templateId`, `language`, `requestedAt`, optional `sendTo`.
2. Three integrations will each have their own queue and message schema on one shared SQS spec, through one overlay per queue that pins `QueueUrl` and adds the message schema. Overlays live with tests and examples, never in `specs/`. The invoices overlay and its proxy config are test data, together in `src/hotline-proxy/testdata/aws/` (Peter, 2026-10-30); the end-to-end test runs that config with only its addresses changed.
3. Stable vendor specs live in `specs/<vendor>/<api>/<version>.openapi.yaml`; the SQS one is generated from AWS's Smithy model, and a test keeps it in step.
4. Tests use a fake SQS, never real AWS; running against AWS is a manual step in the example's README.
5. Other AWS services (S3, DynamoDB) come later.

## Health check decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-health-checks.md](docs/proxy-health-checks.md).

1. `/readyz` says no (503) only before the first config has loaded, while the proxy port is not listening, and from the first stop signal on. Never because a reload failed or a destination is down: one bad shared config edit would otherwise take every copy out of service.
2. `/livez` says no only when the proxy is stuck.
3. After the stop signal the proxy still takes new calls for `--drain-delay` (5 seconds, inside the 25 second stop limit), since Kubernetes needs a moment to stop sending calls and our image has no shell for `sleep`. A second signal skips it.
4. No `listen.admin` means no health checks and one warning; the proxy still runs.
5. Plain HTTP, only `/livez` and `/readyz`, no records. No detail page: the state of each part comes with the proxy's own telemetry, a chapter of its own.
6. The end-to-end tests pass `--drain-delay 0s` unless a scenario sets it, so they stop at once.

## Config reload decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-config-reload.md](docs/proxy-config-reload.md).

1. One mechanism for every file, with two triggers: a check every 5 seconds (identity, time and size; no full read) and SIGHUP for "now". Kubernetes sends no signal when a mounted file changes.
2. Certificates (`listen.tls`, `listen.connect`) use the same check, replacing the check on each new connection.
3. Watched: exactly the files the last load read or tried to read; a missing overlay is watched until it arrives.
4. Live: integrations, apis, specs, overlays, keys, trace settings, certificates. Restart: listen addresses, adding or removing listen.tls or listen.connect, exporters (Envoy's split).
5. `hotline.config.version` = config file name + `+` + first 12 hex of a SHA-256 over the config, specs and overlays (not keys or certificates).
6. A failed reload is a warning in the log, and counted in the proxy's own telemetry (since 2026-10-31).

## Proxy telemetry decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-telemetry.md](docs/proxy-telemetry.md).

1. Metrics only, to `telemetry` only. No `telemetry` section: nothing measured, one warning at start.
2. No label grows with traffic: never by integration, vendor, host, URL or route.
3. Standard names where they exist, even "development" ones: `otel.sdk.*` for queues and sends, `http.server.active_requests`, `http.server.request.duration` (seconds, standard labels, no `http.route`). Our own: `hotline.proxy.overhead` (seconds, by ingress, 8 fixed limits from 0.00005 to 0.1).
4. State as 1 or 0 gauges: `hotline.destination.state`, `hotline.config.reload.last_success`, `hotline.config.restart_needed`; plus `hotline.config.reloads` and `hotline.config.info`.
5. Certificates: `hotline.certificate.not_after`, a time; failed app TLS setups: `hotline.app_tls.failures`.
6. The Go runtime numbers, from the OpenTelemetry contrib module.
7. Every metric can be turned off with `telemetry.disable` (exact name or ending in `*`); an unknown name is a warning.
8. The dropped records and reload warnings stay in the log.

Reworked with Peter on 2026-11-01, from 27 metrics to 14: no unit on 1 or 0 gauges (no `_ratio`); `hotline.exporter.up`, `.queue.size`, `.sent`, `.dropped` replace `otel.sdk.*` and `hotline.destination.state`; config is only `hotline.config.reload_failed` and `hotline.config.restart_needed`; `hotline.proxy.overhead` and the info metrics are gone; four Go numbers; every metric has a description and a unit Grafana can chart. Points 3 and 4 above are replaced by this.

## Trace context decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-trace-context.md](docs/proxy-trace-context.md).

1. Each record carries the app's trace id and flags in the log record's own trace fields, never in attributes of our own.
2. The span id is a new one for the proxy's step; the app's span id is not recorded.
3. No trace id, or one the proxy can't read, gets a new one, so every record has one.
4. `traceFormat` per integration: `w3c` (default), `b3`, `xray` or `jaeger`.
5. `traceForward` per integration, off by default, sends our trace id and span id to the vendor.
6. The trace id, span id and flags are signed with the record.
7. No traces (spans) are sent for now; the call timeline design waits in the project files.
