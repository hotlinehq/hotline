# Sending OpenTelemetry data over HTTP

*Agreed with Peter and built, 2026-10-30. gRPC removed, 2026-11-08. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo.*

OTLP is the OpenTelemetry protocol: the standard way to send telemetry to a collector, a server that receives it and passes it on. Before this change the proxy sent OTLP only over plain gRPC, with no TLS and no login. Many hosted collectors, and many company firewalls, take only HTTP, and nearly all of them need TLS and a login token.

## Config

```yaml
exporters:
  - otlp:                                   # plain HTTP
      endpoint: http://hotline-control-plane.hotline:4318
  - otlp:                                   # TLS, a login token and mutual TLS
      endpoint: https://otel.example.com:4318/otlp
      headers:
        Authorization: { env: OTLP_TOKEN }
        X-Tenant: shop-a
      tls:
        ca: /etc/hotline/otlp/ca.crt
        cert: /etc/hotline/otlp/client.crt
        key: /etc/hotline/otlp/client.key
      compression: none
```

## Decisions (Peter, 2026-10-30)

1. **Only HTTP, as protobuf** (a compact binary format). There is no `protocol` setting since 2026-11-08: gRPC was removed (decision 16), and an old config that still sets `protocol` gets a warning and the field is ignored. JSON over HTTP is not offered: every collector that takes HTTP takes protobuf.
2. **Endpoints** are URLs with `http://` or `https://`; anything else is refused. The proxy adds `/v1/logs`, `/v1/metrics` and `/v1/traces` after its path, so `/otlp` sends to `/otlp/v1/logs`. A user name or password in the URL, or a query, is refused: login goes in `headers`.
3. **TLS follows the scheme**: `https://` means TLS, checked against the system's issuers, the `--trust-file` and `tls.ca`. `tls.cert` with `tls.key` is a client certificate for mutual TLS (both sides show a certificate). `tls` on a plain endpoint is refused.
4. **Login headers.** A value is written out, or read from `{ file: path }` (preferred; a line break at the end is dropped) or `{ env: NAME }`, like `fingerprintKey`. A secret value never reaches a log line or a config message. Files are read once at start.
5. **Compression** is gzip unless `compression: none`: records are wordy and hosted collectors charge by the byte.
6. **Retries.** A busy collector (HTTP `429`, `502`, `503`, `504`, or a lost connection) is tried again within the send's 10 seconds: after about a second at first, then at most every 5 seconds, honoring `Retry-After`. The library's own default waited 5 seconds first, which left room for one retry only. Any other refusal loses the batch.
7. **Lost batches are logged** like a full queue: a warning line with the endpoint, the count and the error, at most once a minute per destination and always at shutdown. Before, a refused batch was lost without a word.
8. **Only the config says how data is sent.** The standard `OTEL_EXPORTER_OTLP_*` environment variables are ignored: the proxy sets every option itself, so the library never falls back to them. The usual `HTTPS_PROXY` and `NO_PROXY` still apply, as for any Go program.
9. **Library.** `otlploghttp` and `otlpmetrichttp`, from the OpenTelemetry Go project, Apache 2.0 license. No new outside project. Since 2026-11-08 the records and spans no longer go through these libraries: the proxy writes them as OTLP itself (decisions below). The counts still do.

## HTTP/2, h2c and the proxy's own sender (Peter, 2026-11-08)

10. **The proxy writes records and spans as OTLP itself**, straight into protobuf bytes, once for every destination, and sends them over HTTP with its own sender. The bytes are standard OTLP: every behaviour test reads them with the official OpenTelemetry types. The counter `hotline.requests` and the proxy's own numbers stay on the OpenTelemetry metrics library. Why, with the measurements: [otlp-encoder-scope.md](otlp-encoder-scope.md).
11. **HTTP/2 over TLS.** An `https://` endpoint uses HTTP/2 when the collector offers it while TLS is set up, and HTTP/1.1 otherwise. Nothing to set.
12. **`h2c: true`** makes a plain `http://` endpoint use HTTP/2 from the first byte (h2c, HTTP/2 without TLS), for records, spans and counts. A collector that does not speak h2c fails every send, warned about and counted like any failed send. Not set or `false`: HTTP/1.1. `h2c` on an `https://` endpoint is refused, and so is any value but `true` or `false`.
13. **Retries keep the collector's wait.** A `Retry-After` the collector sends is kept. The waits have no random part.
14. **Compression** with gzip uses its fastest level for records and spans: in the spike benchmark (docs/otlp-encoder-scope.md) about 20% less processor time per call than the default level, for somewhat more bytes.
15. **A partial success**, where the collector keeps some items and refuses others, counts as sent, as it did with the library.
16. **No gRPC** (Peter, 2026-11-08). The proxy sends only over HTTP, for records, spans and counts, and the `protocol` setting is gone. HTTP with HTTP/2 or h2c gives what gRPC gave (one connection, many sends at once) with one sender instead of two. The proxy's code no longer uses grpc-go, but the program still carries it: the OpenTelemetry metrics HTTP exporter and the official OTLP types bring it in. Dropping it too would mean writing the counts ourselves.

## Tests

`features/export-protocols.feature` drives the proxy against a fake HTTP receiver, plain or over TLS, with or without HTTP/2 and h2c, which remembers paths, headers, compression, client certificates and the HTTP version. The config problems are rows in the broken configs table of `features/config.feature`. The end-to-end tests send over plain HTTP.

## Later

- The `telemetry` section takes the same fields once the proxy's own health data is built.
- Reading a header secret again when its file changes, with config reload.
