# What the proxy sends over OpenTelemetry

This page lists every piece of data the Hotline proxy sends out over OTLP, as the code does it today. OTLP is the OpenTelemetry protocol: the standard way to ship telemetry (records, counts, traces) to a collector, which is a server that receives and forwards it.

The rules come from the spec, `proxy-spec.md` section 4 in the `hotline-strategic` repo. The code lives in `src/hotline-proxy/record.go`, `connect.go` and `internal/export/`. Where the two differ, the last section says so.

In short, the proxy sends three things to every destination under `exporters`:

1. **One log record per call**, named `hotline.request`. It holds everything known about the call.
2. **One counter**, `hotline.requests`, that goes up by one per call.
3. **One trace per call**: the call's steps as spans, each with its start and end. That is where the timings are (Peter, 2026-10-31).

A fourth kind, numbers about the proxy itself, goes only to the `telemetry` destination (last signal below).

## How it is sent

| What | Today |
|---|---|
| Protocol | OTLP over gRPC (the default) or over HTTP as protobuf (`protocol: http/protobuf`), to `/v1/logs`, `/v1/metrics` and `/v1/traces`. Detail: [proxy-otlp-protocols.md](proxy-otlp-protocols.md). |
| Connection to the collector | Plain, or TLS with an `https://` endpoint, with `tls.ca` and a client certificate if needed. Login headers from `headers`. |
| Compression | gzip, unless `compression: none`. |
| Destinations | Every entry under `exporters` gets the same records and counts, each with its own resource labels. Each has its own queue, so one that is down never holds up the others or the calls. |
| How often | Counts every 60 seconds (`metricInterval`), records every 15 seconds (`logInterval`) and spans every 15 seconds (`traceInterval`), each set per exporter. Records and spans also go sooner once 512 are waiting. One data point a minute is what Grafana Cloud's standard pricing covers (Peter, 2026-11-01). |
| Time limit per send | 10 seconds, retries included. Fixed in the program. A busy collector is tried again after about a second at first, then at most every 5 seconds. A send the collector refuses loses its records, counted in a warning line like a full queue. |
| Records per send | At most 512. |
| Queue size | 10 000 records per destination, or `queueSize`. When full, the oldest record is dropped, and a warning log line gives the count, at most once a minute per destination and always once at shutdown. |
| At shutdown | Whatever is queued is sent first, within the shutdown time limit. |
| Sampling | None. Every call is recorded, counted and traced. |
| Counter temporality | Cumulative by default (the running total since start); `temporality: delta` sends only the change since the last send. |

Config example:

```yaml
exporters:
  - otlp:
      endpoint: hotline-control-plane.hotline:4317
  - otlp:
      endpoint: otel-collector.observability:4317
      temporality: delta
      queueSize: 10000
      metricInterval: 60s
      logInterval: 15s
      traceInterval: 15s
  - otlp:
      protocol: http/protobuf
      endpoint: https://otel.example.com:4318
      headers:
        Authorization: { file: /etc/hotline/otlp/token }
      resource:                                   # labels for this destination only
        service.name: invoices-proxy
        service.instance.id: { env: POD_NAME }
        deployment.environment.name: { env: HOTLINE_ENV }
```

## Resource

A resource is the set of labels that describe the sender. Both signals carry the same one. By default it is:

| Attribute | Value |
|---|---|
| `service.name` | `hotline-proxy` |
| `service.version` | the program's version |
| `service.instance.id` | a random ID (a version 4 UUID) made at each start |

Each exporter may add labels, or change these, with its own `resource` section, to say where the proxy runs (Peter, 2026-10-30):

- A value is written out, or read from `{ env: NAME }` or `{ file: path }`, like a login header. A variable that is not set, or a file that cannot be read, refuses the config.
- `service.name` and `service.instance.id` may be changed, for example to the pod's name. `service.version` is always the proxy's own; setting it refuses the config.
- A name may hold only letters, digits, dots and underscores. The OpenTelemetry standard names are best, such as `deployment.environment.name`, `service.namespace` or `cloud.region`.
- The labels go only to that exporter. One without a `resource` section gets the defaults above.

Grafana Cloud turns the dots into underscores, so `deployment.environment.name` shows as `deployment_environment_name`.

The instrumentation scope (the name of the code that made the data) is `hotline-proxy` for both signals.

## Signal 1: the request record

One OpenTelemetry log record per call, sent once the reply has gone back to the app. A call inside a `CONNECT` tunnel gets its own record; a tunnel that opens fine gets none.

- Event name: `hotline.request`.
- Timestamp: when the call reached the proxy.
- Trace id, span id and flags, in the log record's own OpenTelemetry fields, not in attributes. The trace id and flags are the app's, read from its call in the integration's `traceFormat` (`w3c` by default, or `b3`, `xray`, `jaeger`); when the app sent none, the proxy makes a new trace id. The span id is always a new one for the proxy's step. Detail: [proxy-trace-context.md](proxy-trace-context.md).
- A level (the OpenTelemetry severity, its text and number): `INFO` (9) for a normal call; `WARN` (13) when the call or the reply broke the spec (`hotline.schema.request.errors` or `hotline.schema.response.errors`), or the vendor answered with a 4xx; `ERROR` (17) when the vendor answered with a 5xx or the call failed in the proxy (`error.type`). ERROR wins over WARN. Log tools such as Grafana show it as the record's level (Peter, 2026-10-30).
- No body text: everything else is in the attributes. A log tool can build a line from them, for example Grafana's `| line_format "{{.http_request_method}} {{.url_template}} {{.http_response_status_code}}"`.
- Standard names come from the OpenTelemetry semantic conventions (the official list of field names), Go package `semconv` v1.43.0. Hotline-only fields start with `hotline.`.

There are two sizes of record:

- A **full record** is for a call whose integration ID is known and whose host belongs to one of that integration's apis.
- A **minimal record** is for everything else: no ID (`not-passed`), an unknown ID, a host the integration does not use, and calls the proxy refused.

### Fields in every record

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.integration.id` | text | The ID from `Proxy-Authorization`, as sent, or `not-passed`. |
| `hotline.ingress` | text | How the app reached the proxy: `forward_proxy`, `url_in_path`, `host_header` or `connect`. |
| `client.address` | text | The app's IP address, from its connection, never from a header the app could set (Peter, 2026-10-31). Inside a `CONNECT` tunnel it is the address the tunnel came from. |
| `server.address` | text | The vendor host, in lower case. |
| `http.request.method` | text | The method, such as `GET`. |
| `http.response.status_code` | whole number | The status the app got, also when the proxy made it up (`502`, `504`, `403`). |
| `error.type` | text | Only when the call failed. Values are listed below. |
| `network.peer.address` | text | The vendor IP the proxy connected to, after DNS. Only when a connection was made. |
| `hotline.timing.total_us` | whole number, microseconds | From the call reaching the proxy until its last byte went back to the app; on the first call of an app's TLS connection, from the start of the handshake. The same length as the trace's `hotline call` span. Every other timing is in the trace (Peter, 2026-10-31). |

### Fields only in a full record

| Attribute | Type | What it holds |
|---|---|---|
| `url.scheme` | text | `http` or `https`, as sent to the vendor. |
| `server.port` | whole number | The vendor port. |
| `url.full` | text | The vendor address with values replaced by `REDACTED` unless the spec marks them as safe. Left out when the path has no route in the spec. |
| `url.template` | text | The spec's route, such as `/v1/customer_sessions/{sessionId}`. Left out with `url.full`. |
| `network.protocol.version` | text | The HTTP version to the vendor: `1.1` or `2`. Only when the vendor replied. |
| `http.request.body.size` | whole number | Bytes sent to the vendor. |
| `http.response.body.size` | whole number | Bytes received from the vendor. Only when the vendor replied. |
| `tls.protocol.version` | text | Such as `1.3`. Only over TLS. |
| `tls.server.not_after` | text | When the vendor's certificate expires, as an RFC 3339 time in UTC. Only over TLS. |
| `http.request.header.<name>` | list of text | Allowed request headers, name in lower case. |
| `http.response.header.<name>` | list of text | Allowed reply headers, name in lower case. |
| `hotline.client.cancelled` | true or false | The app did not get the whole reply: it gave up or hung up first. |
| `hotline.config.version` | text | The config file's name (without its folder), a `+`, and the first 12 hex characters of a SHA-256 over the config, its specs and overlays, such as `hotline-proxy.aws-shop.yaml+a3f9c21e07b4`. |
| `hotline.schema.*` | see below | What the check against the spec found. |
| any name the spec gives | text, number, or true or false | A body field or call header the spec marks `x-hotline-record`, such as `messaging.destination.name` for SQS's `QueueUrl` and `messaging.operation.name` for its `X-Amz-Target`. Detail: [proxy-body-fields.md](proxy-body-fields.md). |

**Headers.** Kept by default: `content-type`, `content-length`, `date`, `retry-after`, `request-id`, `x-request-id` and any `ratelimit-*`. A header marked `x-hotline-redact: false` in the spec is kept; one marked `true` shows as `REDACTED`. Never recorded: `authorization`, `proxy-authorization`, `cookie`, `set-cookie` and the login headers named in the spec. Every other header is left out, name and all.

### The signature

Every full record has `hotline.signature` and `hotline.signature_key_id`: an HMAC-SHA256 signature, with the integration's `fingerprintKey`, of everything else the record holds, sorted by name. Each body that passed is signed on its own, in `hotline.request.body.signature` and `hotline.response.body.signature`, and the call's trace in `hotline.trace.signature` ([proxy-traces.md](proxy-traces.md)). Minimal records have none. Detail: [proxy-signatures.md](proxy-signatures.md).

### Check results

The proxy checks each full-record call against the vendor's spec, after the reply has gone back to the app.

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.schema.request.errors` | list of maps | Problems with the call, at most 20. |
| `hotline.schema.response.errors` | list of maps | Problems with the reply, at most 20. |
| `hotline.schema.request.errors_total` | whole number | How many request problems there were, only when more than 20. |
| `hotline.schema.response.errors_total` | whole number | The same for the reply. |
| `hotline.schema.skipped` | text | Why part of the check did not run: `no_spec_route`, `body_too_large`, `unsupported_content_type` or `streaming`. |

Each problem is a map with these keys. It never holds a value from the call.

| Key | Always there? | What it holds |
|---|---|---|
| `part` | yes | `path`, `query`, `header`, `cookie`, `body` or `status`. |
| `rule` | yes | One word, such as `required`, `type`, `format`, `enum`, `not_in_spec` or `content_type`. |
| `place` | when known | The name (`page`) or the spot in the body (`$.cartItems[0].quantity`). |
| `expected` | when known | What the spec asks for. |

### Error types

`error.type` is set only when the call failed. The same name goes back to the app in the `Proxy-Status` header.

| Value | When |
|---|---|
| `http_request_error` | The proxy refused the request (`400`, `421`, `501`). |
| `destination_ip_prohibited` | The vendor IP is internal (`403`). |
| `dns_error`, `dns_timeout` | The lookup failed or took too long. |
| `connection_refused`, `destination_unavailable`, `connection_timeout` | Connecting to the vendor failed or took too long. |
| `tls_certificate_error`, `tls_protocol_error` | TLS with the vendor failed. |
| `http_response_timeout` | The vendor did not answer in time. |
| `http_response_incomplete` | The reply was cut off, or went silent for longer than `vendorTimeout`. |
| `tls_untrusted_by_app` | Inside a `CONNECT` tunnel, the app refused the certificate it was shown. |
| `tls_handshake_failed` | Inside a `CONNECT` tunnel, TLS with the app failed for another reason. |

### Refused calls and failed tunnels

A request the proxy reads and refuses still gets a minimal record, with `error.type=http_request_error`. `hotline.ingress` and `server.address` are there only when the proxy could work them out. The only timing is `hotline.timing.total_us`.

A `CONNECT` tunnel whose TLS setup with the app fails gets a minimal record with the ID and host from the `CONNECT`, `http.request.method=CONNECT`, `hotline.ingress=connect` and `hotline.timing.total_us`; its trace has the handshake when it was measured. It has no status code, since no HTTP reply was sent.

Plain HTTP sent to a TLS port is a failed TLS setup, not a request, so it has no record.

## Signal 2: the hotline.requests counter

| Name | Type | Unit | Attributes |
|---|---|---|---|
| `hotline.requests` | Sum that only goes up | `{request}` | `hotline.integration.id`, `url.template`, `http.request.method`, `http.response.status_code`, `error.type` |

Every record above also counts once here, refused calls and failed tunnels included. Each attribute is there only when the record has it, so minimal records carry no `url.template`.

There is no latency histogram and no pass or fail count: the control plane builds those from the records.

## Signal 3: the trace of each call

One trace per call, to every destination, with the same resource labels as the records: `hotline call` for the whole call, and under it a span for each step that happened, such as `routing`, `vendor call` with `dns lookup`, `tcp connect`, `vendor tls handshake`, `send call`, `wait for vendor` and `receive reply` under it, and `reply to app`, with `request spec check` and `reply spec check` for the spec checks. `hotline call` has the record's trace id and span id, and sits under the app's span when the app sent one. Spans hold nothing the record holds: only `hotline.connection.reused` on `vendor call` and `dns.question.name` on `dns lookup`. Detail: [proxy-traces.md](proxy-traces.md).

## Signal 4: the proxy's own numbers

Metrics about the proxy itself, sent only to the `telemetry` destination and never mixed with the records and counts above. Without a `telemetry` section, nothing of this is measured or sent. No number is split by integration, vendor, host or route, so their count stays small however many integrations there are.

- Calls: `http.server.active_requests`, `http.server.request.duration` and `hotline.app_tls.failures`.
- Each exporter: `hotline.exporter.up`, `hotline.exporter.queue.size`, `hotline.exporter.sent` and `hotline.exporter.dropped`.
- Config and certificates: `hotline.config.reload_failed`, `hotline.config.restart_needed` and `hotline.certificate.not_after`.
- The Go runtime's standard set, such as `go.memory.used` and `go.goroutine.count`.

Any of them can be turned off with `telemetry.disable`. Units, labels and details: [proxy-telemetry.md](proxy-telemetry.md).

## What is not sent

- **Bodies, cookies, `Authorization` and `Proxy-Authorization`.** Never sent, by design. Only the body fields a spec marks `x-hotline-record` are copied.
- **The proxy's own log lines.** They go to standard error as JSON, not over OTLP.

## Where the spec and the code differ

They match since 2026-10-31. `hotline.schema.skipped=overloaded` is built since 2026-11-02: a check skipped because the line of waiting checks was full.
