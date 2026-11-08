# Each call's timings as a trace

Agreed with Peter on 2026-10-31. Tests: `src/hotline-proxy/features/timing.feature`, and the trace part of `signature.feature`.

A trace is a set of spans; a span is one step with a start time and an end time. The timings of a call used to be a few number attributes on its record (`hotline.timing.*_us`). They are a trace now: every call gets one, sent over OTLP to every exporter next to its record, so Grafana Cloud stores it in Tempo and draws the call as a timeline (Peter, 2026-10-31).

## The spans of one call

| Span | Kind | From | To |
|---|---|---|---|
| `hotline call` | server | the call reached the proxy, or the app TLS handshake started on a connection's first call | its last byte went back to the app |
| `app tls handshake` | internal | TLS setup with the app started | it ended. First call of a connection only |
| `routing` | internal | the call reached the proxy | the vendor call started: finding the ingress, the integration and the vendor api |
| `vendor call` | client | the vendor call started | the vendor's reply ended |
| `dns lookup`, under `vendor call` | internal | the lookup started | it ended |
| `tcp connect`, under `vendor call` | internal | the first try to connect | connected; several addresses tried count once |
| `vendor tls handshake`, under `vendor call` | internal | TLS setup with the vendor started | it ended |
| `send call`, under `vendor call` | internal | the call had its connection | the call was fully sent |
| `wait for vendor`, under `vendor call` | internal | the call was fully sent | the vendor's first reply byte |
| `receive reply`, under `vendor call` | internal | the first reply byte | the vendor's reply ended |
| `reply to app` | internal | the vendor's reply ended | the last byte went to the app |
| `request spec check` | internal | the call was fully sent | the request's check against the spec ended. Full records only |
| `reply spec check` | internal | the vendor's reply ended | the reply's check ended. Full records with a reply only |
| `wait for check worker`, under each spec check | internal | the check joined the line | a worker took it, or it was skipped as overloaded |

- A step that did not happen has no span: no `dns lookup` for an IP address, no `vendor tls handshake` for plain http, none of the three on a reused connection.
- Each side is checked against the spec as soon as it is whole, alongside the call: the request while the proxy waits for the vendor, the reply while the proxy sends it on (Peter, 2026-10-31). Both spans sit under `hotline call` and may end after it. A request never sent, such as when the vendor refused the connection, is checked when the call ends. The record waits for both checks, but the app never does.
- A refused call, and a `CONNECT` tunnel whose TLS setup failed, have only `hotline call` (with `app tls handshake` for the tunnel, when it was timed).
- Every call gets a trace: there is no sampling, and the app's sampled flag does not change that (Peter, 2026-10-31).
- Times come from the proxy's clock, the same as the record's.

## When a span fails

Only a breakdown fails a span; the vendor's own answer never does, a 404 or a 503 included (Peter, 2026-10-31). Why it failed is in the record's `error.type`; only `wait for check worker` also carries it on the span.

| Span | Status error when |
|---|---|
| `hotline call` | any span below failed, but for the spec checks, or the proxy refused the call |
| `app tls handshake` | TLS setup with the app failed |
| `routing` | never |
| `reply to app` | the app did not get the whole reply: it hung up, or a write to it failed. `hotline.client.cancelled` on the record says so; `vendor call` stays unset |
| `vendor call` | one of its steps failed, or the guard refused an internal address |
| `dns lookup` | the name could not be looked up |
| `tcp connect` | no address could be connected to |
| `vendor tls handshake` | TLS setup with the vendor failed |
| `send call` | the app's body broke off while it was sent |
| `wait for vendor` | the vendor did not answer in time |
| `receive reply` | the vendor's reply broke off |
| `request spec check`, `reply spec check` | that side broke the spec |
| `wait for check worker` | the line of waiting checks was full and this check gave way; the span has `error.type=overloaded`, and its spec check ends there too, unset (Peter, 2026-11-02) |

A failed step ends at the failure. The two spec checks are the only spans set to ok on purpose: when their side passed. A skipped check leaves it unset, since nothing was proven. The check's status itself is only on the record, as `hotline.schema.status`: `valid`, `invalid` or `skipped`.

## Which parent is remote

A span's OTLP flags say whether its parent ran in another program, which OpenTelemetry calls remote. Only `hotline call` has such a parent, the app's own span, when the app sent its trace context. Every other span's parent is a step of the proxy, so it is not remote. Until 2026-11-08 every span said its parent was remote (Peter, 2026-11-08).

## Nothing on both

Each piece of data is either on the trace or on the record, never on both, unless there is a good reason (Peter, 2026-10-31).

- Spans carry only their name, kind, start, end and status.
- Two attributes moved from the record to the trace: `hotline.connection.reused` on `vendor call`, and `dns.question.name` on `dns lookup`.
- Method, route, status, integration, host, vendor IP and TLS facts stay only on the record. To find a call's timeline, search for its record and jump to its trace.
- The links are on both: the record's trace id is the trace's, and the record's span id is the id of `hotline call`.
- The record keeps `hotline.timing.total_us`, the length of `hotline call` (Peter, 2026-10-31: fine to have). Every other `hotline.timing.*_us` is gone. On the first call of an app's TLS connection, it counts from the start of the handshake.

## Ids and the app's trace

- When the app sent trace context, `hotline call` sits under the app's span, so the proxy's steps show inside the app's own trace. The app's span id is not on the record.
- Without it, `hotline call` has no parent and the trace id is a new one, as on the record.
- With `traceForward: true`, the vendor gets the id of `vendor call`, so the vendor's own spans sit under that step.

## Signed

The trace of a full record is signed with the integration's key, the same as the record (Peter, 2026-10-31). The signature goes on the record as `hotline.trace.signature`, so the record's own signature covers it, as with the body signatures. Minimal records are not signed, so their traces are not either.

The signed bytes are made the same way as the record's ([proxy-signatures.md](proxy-signatures.md)): entries of a name and a typed value, each piece its length then its bytes, nothing quoted or escaped (Peter, 2026-11-02). First the entry `trace.trace_id` (text, hex). Then, for each span in start order, a tie going by span id: `span.span_id` and `span.parent_span_id` (text, hex; empty for none), `span.name`, `span.kind` (`server`, `client` or `internal`), `span.start_time` and `span.end_time` (whole numbers, nanoseconds since 1970), `span.status` (`unset`, `ok` or `error`), `span.attribute_count` (a whole number), then its attributes as entries, in name order.

To check a call, take its record and its spans (found by trace id), and rebuild both signatures with the key.

## How it is sent

- OTLP traces over gRPC, or over HTTP to `/v1/traces`, with each exporter's endpoint, TLS, login headers, gzip, resource labels and timing.
- Each destination has a queue of spans next to its queue of records, of the same `queueSize`, counted in spans. A full queue drops its oldest span and warns, at most once a minute.
- The proxy's own telemetry counts them like records, with `hotline.signal=span` on `hotline.exporter.queue.size`, `.sent` and `.dropped`.
- The proxy's own time is the `routing` plus `reply to app` steps; since 2026-11-01 there is no metric for it, as every trace carries it.

## Where it lives

- `trace.go`: the spans of a call, and the trace signature.
- `internal/export/trace.go` and `wire.go`: the proxy writes each span as OTLP protobuf itself, since 2026-11-08 ([otlp-encoder-scope.md](otlp-encoder-scope.md)); before, it went through the SDK's `tracetest.SpanStub` and trace senders.
- `internal/export/queue.go`: one queue type for records and spans.
- New modules, at first: `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` and `otlptracehttp`, Apache-2.0, the OpenTelemetry trace senders. Removed again on 2026-11-08.
