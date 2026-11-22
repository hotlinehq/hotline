# What the proxy sends over OpenTelemetry

This page lists every piece of data the Hotline proxy sends out over OTLP, as the code does it today. OTLP is the OpenTelemetry protocol: the standard way to ship telemetry (records, traces, metrics) to a collector, which is a server that receives and forwards it.

The rules come from the spec, `proxy-spec.md` section 4 in the `hotline-strategic` repo. The code lives in `src/hotline-proxy/record.go`, `connect.go` and `internal/export/`. Where the two differ, the last section says so.

In short, the proxy sends two things to every exporter under `exporters`:

1. **One log record per call**, named `hotline.request`. It holds everything known about the call.
2. **One trace per call**: the call's steps as spans, each with its start and end. That is where the timings are (Peter, 2026-10-31).

Exporters get no metrics. The control plane works out counts and latencies from the records (Peter, 2026-11-21): the `hotline.requests` counter, one per call by integration, route, method, status and error, was removed then. A third kind, numbers about the proxy itself, goes only to the `telemetry` section (last signal below).

## How it is sent

| What | Today |
|---|---|
| Protocol | OTLP over HTTP as protobuf, to `/v1/logs` and `/v1/traces`. No gRPC since 2026-11-08. Over TLS, HTTP/2 when the collector offers it; to a plain endpoint, HTTP/1.1, or HTTP/2 with `h2c: true`. The proxy writes records and spans as OTLP itself. Detail: [proxy-otlp-protocols.md](proxy-otlp-protocols.md). |
| Connection to the collector | Plain, or TLS with an `https://` endpoint, with `tls.ca` and a client certificate if needed. Login headers from `headers`. |
| Compression | gzip, unless `compression: none`; records and spans at gzip's fastest level. |
| Exporters | Every entry under `exporters` gets the same records and spans, each with its own resource labels. Each has its own queue, so one that is down never holds up the others or the calls. |
| How often | Records every 15 seconds (`logInterval`) and spans every 15 seconds (`traceInterval`), each set per exporter. Both also go sooner once 512 are waiting. `metricInterval` and `temporality` under an exporter are ignored, with no warning (Peter, 2026-11-21). |
| Time limit per send | 10 seconds, retries included. Fixed in the program. A busy collector is tried again after about a second at first, then at most every 5 seconds. A send the collector refuses loses its records, counted in a warning line like a full queue. |
| Records per send | At most 512. |
| Queue size | 10 000 records per exporter, or `queueSize`. When full, the new record is refused and what waits is kept (Peter, 2026-11-09), and a warning log line gives the count, at most once a minute per exporter and always once at shutdown. |
| At shutdown | Whatever is queued is sent first, within the shutdown time limit. |
| Sampling | None. Every call is recorded and traced. |

A `file` exporter writes the same records and traces to a local folder instead, one line per call: [proxy-file-export.md](proxy-file-export.md). A `console` exporter prints two short lines per call to standard output: [proxy-console-export.md](proxy-console-export.md).

Config example:

```yaml
exporters:
  - otlp:
      endpoint: http://hotline-control-plane.hotline:4318
  - otlp:
      endpoint: http://otel-collector.observability:4318
      queueSize: 10000
      logInterval: 15s
      traceInterval: 15s
  - otlp:
      endpoint: https://otel.example.com:4318
      headers:
        Authorization: { file: /etc/hotline/otlp/token }
      resource:                                   # labels for this exporter only
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

One OpenTelemetry log record per call, sent once the response has gone back to the app. A call inside a `CONNECT` tunnel gets its own record; a tunnel that opens fine gets none.

A WebSocket's opening call is recorded as soon as its `101` reaches the app, like any other call: its response has no body (`http.response.body.size` is 0 and `hotline.response.body.hash` is the hash of no bytes), and its time ends at the `101`. What follows the `101` belongs to the session, which gets a record of its own when it ends: see [WebSocket sessions](#websocket-sessions) (Peter, 2026-11-18). Before, a WebSocket had one record, made at its end: it counted the vendor's frames as the response body and the whole session as the call's time.

A streamed response whose spec gives its media type an `itemSchema` (OpenAPI 3.2), such as Anthropic's `text/event-stream`, or JSON Lines or a JSON sequence, is recorded the same way: once its headers reach the app, with no response body and its time ending there. Its events belong to the stream, which gets a record of its own when it ends, and each event gets a record too: see [Streamed responses](#streamed-responses) (Peter, 2026-11-21, M2, Q1 to Q3).

- Event name: `hotline.request`.
- Timestamp: when the call reached the proxy.
- Trace id, span id and flags, in the log record's own OpenTelemetry fields, not in attributes. The trace id and flags are the app's, read from its call in the integration's `traceFormat` (`w3c` by default, or `b3`, `xray`, `jaeger`); when the app sent none, the proxy makes a new trace id. The span id is always a new one for the proxy's step. Detail: [proxy-trace-context.md](proxy-trace-context.md).
- A level (the OpenTelemetry severity, its text and number): `INFO` (9) for a normal call; `WARN` (13) when the call or the response broke the spec (`hotline.schema.request.errors` or `hotline.schema.response.errors`), or the vendor answered with a 4xx; `ERROR` (17) when the vendor answered with a 5xx or the call failed in the proxy (`error.type`). ERROR wins over WARN. Log tools such as Grafana show it as the record's level (Peter, 2026-10-30).
- No body text: everything else is in the attributes. A log tool can build a line from them, for example Grafana's `| line_format "{{.http_request_method}} {{.url_template}} {{.http_response_status_code}}"`.
- Standard names come from the OpenTelemetry semantic conventions (the official list of field names), Go package `semconv` v1.43.0. Hotline-only fields start with `hotline.`.

There are two sizes of record:

- A **full record** is for a call whose integration ID is known and whose host belongs to one of that integration's apis.
- A **minimal record** is for everything else: no ID or an ID the config does not name (both recorded as `not-passed`, Peter, 2026-11-17), a host the integration does not use, and calls the proxy refused. It carries the sizes of the bodies that passed, `http.request.body.size` and `http.response.body.size` (Peter, 2026-11-08), counted as they stream through and never kept. The response's size is there only when a response came; calls the proxy refused have neither. With `urlFull: true` on an integration, or on the `not-passed` entry for every integration that says nothing, a minimal record also carries `url.full` (Peter, 2026-11-08): the address as the app sent it, query included and nothing redacted, since without a spec the proxy cannot know what is secret. It is off by default, and `content: full` switches it on too (Peter, 2026-11-12), since the file already gets the whole address; refused calls and failed tunnels never carry it.

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
| `network.protocol.version` | text | The HTTP version to the vendor: `1.1` or `2`. Only when the vendor responded. |
| `http.request.body.size` | whole number | Bytes sent to the vendor. |
| `http.response.body.size` | whole number | Bytes received from the vendor. Only when the vendor responded. |
| `tls.protocol.version` | text | Such as `1.3`. Only over TLS. |
| `tls.server.not_after` | text | When the vendor's certificate expires, as an RFC 3339 time in UTC. Only over TLS. |
| `http.request.header.<name>` | list of text | Allowed request headers, name in lower case. |
| `http.response.header.<name>` | list of text | Allowed response headers, name in lower case. |
| `hotline.client.cancelled` | true or false | The app did not get the whole response: it gave up or hung up first. |
| `hotline.config.version` | text | The config file's name (without its folder), a `+`, and the first 12 hex characters of a SHA-256 over the config, its specs and overlays, such as `hotline-proxy.aws-shop.yaml+a3f9c21e07b4`. |
| `hotline.schema.*` | see below | What the check against the spec found. |
| any name the spec gives | text, number, or true or false | A body field or call header the spec marks `x-hotline-record`, such as `messaging.destination.name` for SQS's `QueueUrl` and `messaging.operation.name` for its `X-Amz-Target`. Detail: [proxy-body-fields.md](proxy-body-fields.md). |

**Headers.** Kept by default: `content-type`, `content-length`, `date`, `retry-after`, `request-id`, `x-request-id` and any `ratelimit-*`. A header marked `x-hotline-redact: false` in the spec is kept; one marked `true` shows as `REDACTED`. Never recorded: `authorization`, `proxy-authorization`, `cookie`, `set-cookie` and the login headers named in the spec. Every other header is left out, name and all.

### The ledger

Every record, minimal or full, has `hotline.record.hash`, a SHA-256 hash of everything else it holds, and `hotline.batch.id` and `hotline.batch.index`, its place in a signed batch. Records with an HTTP exchange have `hotline.request.body.hash` and `hotline.response.body.hash`, and every record with a trace has `hotline.trace.hash`. Every 10 seconds each integration's batch leaves as a record of its own, `event.name = hotline.ledger.batch`, through every exporter. Detail: [proxy-ledger.md](proxy-ledger.md).

### Check results

The proxy checks each full-record call against the vendor's spec, after the response has gone back to the app.

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.schema.request.errors` | list of maps | Problems with the call, at most 20. |
| `hotline.schema.response.errors` | list of maps | Problems with the response, at most 20. |
| `hotline.schema.request.errors_total` | whole number | How many request problems there were, only when more than 20. |
| `hotline.schema.response.errors_total` | whole number | The same for the response. |
| `hotline.schema.skipped` | text | Why part of the check did not run: `no_spec_route`, `body_too_large`, `unsupported_content_type` or `streaming` (a streamed response whose spec gives no `itemSchema`). |

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
| `http_response_incomplete` | The response was cut off, or went silent for longer than `vendorTimeout`. |
| `tls_untrusted_by_app` | Inside a `CONNECT` tunnel, the app refused the certificate it was shown. |
| `tls_handshake_failed` | Inside a `CONNECT` tunnel, TLS with the app failed for another reason. |

### Refused calls and failed tunnels

A request the proxy reads and refuses still gets a minimal record, with `error.type=http_request_error`. `hotline.ingress` and `server.address` are there only when the proxy could work them out. The only timing is `hotline.timing.total_us`.

A `CONNECT` tunnel whose TLS setup with the app fails gets a minimal record with the ID and host from the `CONNECT`, `http.request.method=CONNECT`, `hotline.ingress=connect` and `hotline.timing.total_us`; its trace has the handshake when it was measured. It has no status code, since no HTTP response was sent.

Plain HTTP sent to a TLS port is a failed TLS setup, not a request, so it has no record.

### WebSocket sessions

A WebSocket, after its `101`, gets one more log record when it ends, whichever side ends it (Peter, 2026-11-18, WebSocket milestone 1). It counts and hashes the bytes each side sent. When the opening call's route lists its messages (`x-async-api`, [spec-marks.md](spec-marks.md)), each message gets a record of its own too (see [WebSocket messages](#websocket-messages)), and the session record also says how the session ended and counts its pings and pongs (Peter, 2026-11-19, slice 2).

- Event name: `hotline.websocket.session`.
- Timestamp: when the `101` reached the app.
- Trace id and flags: the opening call's. Span id: the `websocket session` span's (see [Signal 2](#signal-2-the-trace-of-each-call)).
- Level: `INFO`.
- It goes into the ledger like a call's record, with `hotline.record.hash`, `hotline.trace.hash` and its place in a batch, and `hotline-proxy verify` checks it.
- A session still open when the proxy stops is ended and recorded, within the stop's time.

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.integration.id`, `hotline.ingress`, `client.address`, `server.address` | text | As in the opening call's record. A captured session also names its program, as the call does. |
| `hotline.websocket.from_app.size` | number | Bytes the app sent after the `101`: the WebSocket's frames as they passed, compressed or not, without TLS. |
| `hotline.websocket.from_vendor.size` | number | Bytes the vendor sent after the `101`, counted the same way. |
| `hotline.websocket.from_app.hash` | text | The hash of those bytes, made as a body's hash is, with the side `from_app` and the session's trace and span ids ([proxy-ledger.md](proxy-ledger.md)). |
| `hotline.websocket.from_vendor.hash` | text | The same for the vendor's bytes, with the side `from_vendor`. |
| `hotline.websocket.duration_us` | number | How long the session was open, from the `101` reaching the app to its end, in whole microseconds. |

Only when the route lists its messages, on the record and on the `websocket session` span:

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.websocket.close.from` | text | Who sent the session's first close message, `app` or `vendor`; `none` when no close message came. |
| `hotline.websocket.close.code`, `hotline.websocket.close.reason` | number, text | That close message's code and reason, when it has them, such as 1007 and `API key not valid`. |
| `hotline.websocket.closed_first` | text | With no close message: whose connection closed first, `app` or `vendor`. |
| `hotline.websocket.from_app.pings`, `.pongs`, `hotline.websocket.from_vendor.pings`, `.pongs` | number | The pings and pongs each side sent. They get no record and no span. |
| `hotline.websocket.unread_from_index` | number | When a side's messages could not all be read, the index of the first one that was not. |

### WebSocket messages

Each message of a session whose route lists its messages gets one log record, sent as soon as the message has passed and its check has ended (Peter, 2026-11-19, WebSocket slice 2). Pings, pongs and close messages are not messages.

- Event name: `hotline.websocket.message`.
- Timestamp: when the message's first byte passed.
- Trace id and flags: the opening call's. Span id: the message's own `websocket message` span.
- It goes into the ledger like a call's record, and `hotline-proxy verify` checks it. The console shows no line for it.

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.integration.id`, `hotline.ingress` | text | As in the session record. |
| `hotline.websocket.message.index` | number | 1, 2, 3 in the order the messages' last bytes passed, both directions together. |
| `hotline.websocket.message.direction` | text | `from_app` or `from_vendor`. |
| `hotline.websocket.message.name` | text | The message, by its name in `components/x-async-api/messages`, whose payload schema's `x-hotline-match` fields it matched, or `unknown`. |
| `hotline.websocket.operation` | text | The operation the message belongs to, such as `toolCall`. None for an `unknown` message. Kept even though the spec could supply it, so the data store can work from the traces alone (Peter, 2026-11-21, Q6). |
| `hotline.websocket.message.role` | text | `request`, `reply`, `answer` or `push`, from where the spec lists the message ([spec-marks.md](spec-marks.md), rule 8; Q9). None for an `unknown` message. |
| `messaging.message.conversation_id` | text | The message's correlation id, from its `correlationId`, when it has one and the value is text or a number (a number is written as its text). A request and its replies share it, so the store groups them by it (Q3, Q4). |
| `hotline.websocket.message.size` | number | The message's bytes as sent, all its frames' payloads together, without the frame headers. |
| `hotline.websocket.message.unpacked_size` | number | For a compressed message, its size once unpacked. |
| `hotline.schema.*` | | The spec check, as for a call's body: from the app under `request`, from the vendor under `response`. `hotline.schema.skipped` is `body_too_large` over 2 MiB after unpacking, `overloaded` when its check, or its reading, gave way, `unsupported_content_type` for a side that is not JSON. None for an `unknown` message, or when the api's `checks` leave out `messages`. |
| fields marked `x-hotline-record` | | Copied as sent, such as Gemini's `usageMetadata.totalTokenCount`; never added up. |

### Streamed responses

A streamed response whose media type has an `itemSchema` in the spec gets one more log record when the stream ends, however it ends (Peter, 2026-11-21, M2, Q3). That is `text/event-stream`, Server-Sent Events, read by the rules of the HTML standard; JSON Lines (`application/jsonl`, `application/x-ndjson`), one stream item per line; and JSON sequences (`application/json-seq` and every `+json-seq` type), one stream item per record started by the record separator, byte 0x1E. A response sent whole, or one whose spec gives no `itemSchema`, has no stream record and is skipped as `streaming`, as before.

- Event name: `hotline.stream`.
- Timestamp: when the response's headers reached the app.
- Trace id and flags: the call's. Span id: the `stream` span's.
- It goes into the ledger like a call's record, and `hotline-proxy verify` checks it.
- A stream still open when the proxy stops is ended and recorded as `cut`, within the stop's time.
- The console shows it in two lines, like a WebSocket session: who and where, then `<< ended`, its items, how many broke the spec, its size and how long it was open (Q5).

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.integration.id`, `hotline.ingress`, `client.address`, `server.address` | text | As in the call's record. A captured stream also names its program, as the call does. |
| `hotline.stream.items` | number | The stream items delivered: events with data, each ended by a blank line, or lines or records that are not empty. An event left open at the end is not counted; a last line or record is, unless the stream was cut. |
| `hotline.stream.items_invalid` | number | The items whose check found the spec broken. |
| `hotline.stream.size` | number | Bytes the vendor sent after the headers, as they passed, without TLS. |
| `hotline.stream.hash` | text | The hash of those bytes, made as a body's hash is, with the side `stream` and the stream's trace and span ids ([proxy-ledger.md](proxy-ledger.md)). |
| `hotline.stream.duration_us` | number | How long the stream was open, from its headers reaching the app to its end, in whole microseconds. |
| `hotline.stream.end` | text | `complete` when the vendor ended the stream and the app got all of it; `cut` when the vendor broke off, the app left or the proxy stopped. |

Under `content: full`, the file exporters also get `hotline.content.stream.body`, the stream's first `maxBodySize` bytes as base64, with `hotline.content.stream.body.truncated` true when it was longer, and `hotline.content.hash` over that flag. `hotline-proxy verify` checks the kept stream against `hotline.stream.hash` ([proxy-file-export.md](proxy-file-export.md)).

### Stream items

Each stream item of a streamed response (an event, a JSON line or a JSON sequence record) gets one log record, sent once it has passed and its check has ended, after the call's record (Peter, 2026-11-21, M2, Q1). An event is checked against the `itemSchema` as the object OpenAPI 3.2 gives it: `data` as text, and `event`, `id` and `retry` when the event sets them; a line or a record is checked as the JSON it holds. The events go on to the app as they come and never wait for a check.

- Event name: `hotline.stream.item`.
- Timestamp: when the event's first byte passed.
- Trace id and flags: the call's. Span id: the item's own `stream item` span.
- It goes into the ledger like a call's record, and `hotline-proxy verify` checks it. The console shows no line for it.

| Attribute | Type | What it holds |
|---|---|---|
| `hotline.integration.id`, `hotline.ingress` | text | As in the stream record. |
| `hotline.stream.item.index` | number | 1, 2, 3 in the order the stream items ended. |
| `hotline.stream.item.size` | number | The stream item's bytes as sent: an event with its blank line, a line with its line end, a record with its separator. |
| `hotline.schema.*` | | The check, under `response`. `hotline.schema.skipped` is `body_too_large` for a stream item over 2 MiB, and `overloaded` when the spec check line was full. None when the api's `checks` leave out `responseBody`. |
| fields marked `x-hotline-record` | | Copied as sent, such as Anthropic's event name, `anthropic.stream.event`. |

## Signal 2: the trace of each call

One trace per call, to every exporter, with the same resource labels as the records: `hotline call` for the whole call, and under it a span for each step that happened, such as `routing`, `vendor call` with `dns lookup`, `tcp connect`, `vendor tls handshake`, `send call`, `wait for vendor` and `receive response` under it, and `response to app`, with `request spec check` and `response spec check` for the spec checks. `hotline call` has the record's trace id and span id, and sits under the app's span when the app sent one. Spans hold nothing the record holds: only `hotline.connection.reused` on `vendor call` and `dns.question.name` on `dns lookup`. The one exception is `websocket message`, which also carries its record's `hotline.websocket.operation`, `hotline.websocket.message.role` and `messaging.message.conversation_id`, so a query in the trace store can group a request with its replies (Peter, 2026-11-21, Q4). A WebSocket session has one span more, `websocket session`, sent with its own record: a child of `hotline call`, from the `101` reaching the app, where `hotline call` ends, to the session's end (Peter, 2026-11-18). Each of its messages has a `websocket message` span under `websocket session`, from the message's first byte to its last, sent with the message's record, with `message spec check` and `wait for check worker` under it when it was checked (Peter, 2026-11-19). A streamed response is the same: a `stream` span under `hotline call`, from the headers reaching the app to the stream's end, failed when the stream was cut, and under it a `stream item` span for each event, from its first byte to its last, with `item spec check` and `wait for check worker` under it when it was checked (Peter, 2026-11-21, M2, Q2). Detail: [proxy-traces.md](proxy-traces.md).

## Signal 3: the proxy's own numbers

Metrics about the proxy itself, sent only to the `telemetry` section and never mixed with the records and traces above. Without a `telemetry` section, nothing of this is measured or sent. No number is split by integration, vendor, host or route, so their count stays small however many integrations there are.

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
