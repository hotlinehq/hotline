# Trace context on records

Agreed with Peter on 2026-10-31. Tests: `src/hotline-proxy/features/trace-context.feature`.

A trace is all the steps of one request, across services, tied together by one trace id. A span is one of those steps, with its own span id. When an app's call carries a trace id, the proxy puts it on the call's record, so Grafana can show the record next to the app's own logs and traces.

## Where it goes

The ids go in the log record's own OpenTelemetry fields, not in attributes, so no names of our own are needed (Peter, 2026-10-31). Grafana Cloud shows them as `trace_id`, `span_id` and `flags`, and links logs to traces by `trace_id`.

| Field | Holds |
|---|---|
| `TraceId` | The app's trace id, or a new one when the app sent none the proxy can read. |
| `SpanId` | A new span id for the proxy's own step, one per call: the id of the call's `hotline call` span. |
| `TraceFlags` | The app's flags. The flags say whether the trace is sampled, that is kept by whoever records traces. A new trace id is sampled. |

The app's own span id is not on the record: there is no standard name for it on a log record (Peter, 2026-10-31). It is the parent of the call's trace instead, so the proxy's steps show inside the app's own trace ([proxy-traces.md](proxy-traces.md)). In Grafana the record's span id opens that trace.

Every record has the three fields: full records, refused calls and failed tunnels alike. They are covered by the record's hash; see [proxy-ledger.md](proxy-ledger.md).

## Formats

Each integration says how its app sends the trace context, with `traceFormat`. The default is `w3c`. The `not-passed` entry may set it too, for calls without an integration ID.

| `traceFormat` | Header the app sends |
|---|---|
| `w3c` (default) | `traceparent: 00-<32 hex trace id>-<16 hex span id>-<2 hex flags>` |
| `b3` | `b3: <trace id>-<span id>-<sampled>`, or the headers `X-B3-TraceId`, `X-B3-SpanId`, `X-B3-Sampled` and `X-B3-Flags` |
| `xray` | `X-Amzn-Trace-Id: Root=1-<8 hex>-<24 hex>;Parent=<16 hex>;Sampled=1` |
| `jaeger` | `uber-trace-id: <trace id>:<span id>:<parent>:<flags>` |

A header that is missing, sent twice or broken in any way counts as no trace context: the proxy makes a new random trace id. For `xray`, its first 8 hex digits are the time in seconds, as X-Ray needs. A 64-bit trace id from b3 or Jaeger is padded with zeros in front.

## Passing it on

By default the proxy only reads the trace headers and the vendor gets the call unchanged. With `traceForward: true` on an integration, the vendor gets the proxy's trace id and the id of the call's `vendor call` span, in the same format, in place of the app's, so the vendor's own spans sit under that step:

```yaml
integrations:
  - id: invoices
    traceFormat: xray
    traceForward: true   # off by default
    apis: [aws-sqs]
```

For `b3` the proxy writes the `X-B3-*` headers and removes a `b3` header.
