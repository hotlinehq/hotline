# The proxy's own telemetry

Agreed with Peter on 2026-10-31, reworked with him on 2026-11-01 (from 27 metrics to 14). Tests: `src/hotline-proxy/features/telemetry.feature`. Code: `src/hotline-proxy/own.go`, `internal/export/own.go` and `internal/config/telemetry.go`.

## What it does

The proxy sends numbers about itself as OpenTelemetry metrics (numbers sent once a minute, or as often as `telemetry.otlp.metricInterval` says) to the `telemetry` destination, and nowhere else: never mixed with the records and counts of vendor calls. An operator builds a Grafana board and alerts on them, such as "an exporter has been down for 5 minutes", "a reload failed" or "the certificate expires within 7 days". This replaces a detail page on `/readyz` (health checks, decision 5).

```yaml
telemetry:
  otlp:                       # the same settings as one entry under exporters
    endpoint: http://otel-collector.observability:4318
    resource:                 # optional; labels on every metric
      service.name: payments-proxy
      deployment.environment.name: prod
  disable: [http.server.request.duration, go.*]   # optional; metrics to turn off
```

## What is sent

No number is split by integration, vendor, host, URL or route: a proxy may hold a million integrations and call any vendor, and the numbers stay as few as with one. Labels come only from short fixed lists (the 4 ingresses, status codes, error types, signals, drop reasons) or from the config's few exporters and certificates. The detail for each integration stays in the records, for the control plane.

Every metric is sent with a description, its help text in Grafana, and a unit Grafana can chart: seconds (`s`), bytes (`By`), or a word in braces naming what is counted, such as `{request}`. A gauge that is 1 or 0 has no unit, so its Prometheus name gets no `_ratio` ending (Peter, 2026-11-01). The last column is the name Grafana Cloud shows: dots become underscores, a unit adds `_seconds` or `_bytes`, and a counter adds `_total`.

| Metric | Kind | Unit | Labels | What it says | In Grafana |
|---|---|---|---|---|---|
| `http.server.active_requests` | up and down count | `{request}` | `hotline.ingress` | calls from apps that the proxy has started and not yet finished; one that keeps growing means calls are stuck, often on a vendor that does not answer | `http_server_active_requests` |
| `http.server.request.duration` | histogram | `s` | `hotline.ingress`, `http.response.status_code`, `error.type` | how long each call took, as the app sees it, from arrival until the last byte went back to the app; vendor time included | `http_server_request_duration_seconds` |
| `hotline.app_tls.failures` | count | `{connection}` | `error.type`, and `hotline.ingress=connect` inside a tunnel | app connections that failed to set up TLS with the proxy, before any call, so no record exists for them | `hotline_app_tls_failures_total` |
| `hotline.exporter.up` | gauge, 1 or 0 | none | `hotline.exporter` | 1 while sends to the exporter work, 0 from the first failed send until one works again | `hotline_exporter_up` |
| `hotline.exporter.queue.size` | up and down count | `{item}` | `hotline.exporter`, `hotline.signal` (`record`, `span`) | records or spans waiting to be sent | `hotline_exporter_queue_size` |
| `hotline.exporter.sent` | count | `{item}` | `hotline.exporter`, `hotline.signal` | records, spans or counts delivered | `hotline_exporter_sent_total` |
| `hotline.exporter.dropped` | count | `{item}` | `hotline.exporter`, `hotline.signal`, `hotline.drop.reason` (`queue_full`, `send_failed`) | records, spans or counts lost: the queue overflowed, or the receiver refused the send or could not be reached | `hotline_exporter_dropped_total` |
| `hotline.config.reload_failed` | gauge, 1 or 0 | none | none | 1 while the last reload failed and the proxy still runs the config before it | `hotline_config_reload_failed` |
| `hotline.config.restart_needed` | gauge, 1 or 0 | none | none | 1 while a changed listen address, exporter or telemetry setting waits for a restart | `hotline_config_restart_needed` |
| `hotline.certificate.not_after` | gauge | `s` | `hotline.certificate.use` (`<listener>.<part>`, such as `apps.tls`, `apps.connect` or `admin.tls`) | when the proxy's own certificate or the CONNECT issuer expires, in seconds since 1970 | `hotline_certificate_not_after_seconds` |
| `hotline.spec_check.queue.memory.usage` | up and down count | `By` | none | memory the bodies of spec checks waiting for a worker hold now; at most `specChecks.queueMemory` | `hotline_spec_check_queue_memory_usage_bytes` |
| `hotline.spec_check.queue.size` | up and down count | `{check}` | none | spec checks waiting for a worker now | `hotline_spec_check_queue_size` |
| `hotline.spec_check.dropped` | count | `{check}` | none | spec checks skipped because the line was full; their records say `hotline.schema.skipped=overloaded` | `hotline_spec_check_dropped_total` |
| `go.memory.used`, `go.memory.limit`, `go.memory.gc.goal`, `go.goroutine.count` | gauges | `By`, `{goroutine}` | as the standard says | the Go runtime's memory and goroutines; the limit only when `GOMEMLIMIT` is set | `go_memory_used_bytes` and so on |
| `go.memory.allocated` | count | `By` | as the standard says | memory the program handed out since it started, counting memory freed again; the load test divides its growth by the calls made | `go_memory_allocated_bytes_total` |

`hotline.exporter` is the exporter's endpoint as the config writes it. `hotline.signal` is `record`, `span` or `count` (the `hotline.requests` numbers).

`error.type` on a call is the record's error type, such as `dns_error`. On a failed TLS setup it is `tls_untrusted_by_app` (the app refused the certificate), `plain_http` (plain HTTP on the TLS port), `tls_version` (only TLS versions the proxy does not take) or `tls_handshake_failed`.

`http.server.request.duration` has the standard's suggested ranges, 0.005 to 10 seconds (14 limits). A set of labels nobody used sends nothing.

**Labels on every metric.** The `resource` labels of the `telemetry` section, plus `service.name` (`hotline-proxy` unless set there), `service.version` and `service.instance.id`, which the proxy sets. Grafana Cloud, like Prometheus, turns `service.name` into `job` and `service.instance.id` into `instance` on every metric, and puts the other resource labels in `target_info`.

**Charts this allows.** Calls in flight per ingress; slow calls per ingress and status; TLS failures by reason; each exporter up or down, its queue, and the share of items lost (`dropped` against `sent`); a failed reload or a waiting restart as red or green tiles; days until each certificate expires, as `(hotline_certificate_not_after_seconds - time()) / 86400`; memory used against its limit.

## Reworked (Peter, 2026-11-01)

The first set had 27 metrics, many overlapping. Reworked to 14:

- **No unit on the 1 or 0 gauges,** so Prometheus adds no `_ratio`.
- **`http.server.active_requests` stays,** by ingress only: it shows calls stuck on a vendor that hangs, which the duration histogram only sees once they end.
- **`http.server.request.duration`** is split by ingress, status code and error type only; method and scheme are gone.
- **`hotline.proxy.overhead` is gone:** every record and trace already carries the proxy's own time on each call.
- **The ten `otel.sdk.*` metrics and `hotline.destination.state` are replaced by four `hotline.exporter.*` ones,** named after the config's word, `exporters`, all with the same exporter label, so one exporter's numbers line up on one board row. The queue's capacity is gone: it is the `queueSize` setting.
- **Config is two 1 or 0 gauges,** `hotline.config.reload_failed` and `hotline.config.restart_needed`. `hotline.config.info`, `hotline.config.reloads` and `hotline.config.reload.last_success` are gone, and so are the TLS and CONNECT flags, which are in the config.
- **Five Go numbers,** not eight: `go.memory.allocated` was added on 2026-11-02 for the load test. `go.memory.allocations`, `go.processor.limit` and `go.config.gogc` are not sent.
- **Every metric has a description and a unit** Grafana can chart.

## Decisions (Peter, 2026-10-31)

Changed by the rework above where they name metrics: decisions 1, 2, 3, 5 and 8.

1. **A destination's state is a 1 or 0 gauge,** next to the counts of sent and lost records, like HAProxy's server state and Envoy's `server.live`. No time of the last good send.
2. **The queue and send numbers use OpenTelemetry's SDK metric names,** though the standard still marks them "development". If the standard renames them, we follow and say so in the release notes. Numbers with no standard name use `hotline.`.
3. **The config's state is a count plus two 1 or 0 gauges:** `hotline.config.reloads`, `hotline.config.reload.last_success` (as Prometheus does) and `hotline.config.restart_needed`. A change put back as it was clears `restart_needed`, as it is compared with the config the proxy started with.
4. **Without a `telemetry` section nothing of the proxy's own is measured or sent,** and the proxy logs one warning at start, like a missing admin port.
5. **The proxy's own time is a small histogram,** `hotline.proxy.overhead`, by ingress only, in seconds (Peter first asked for microseconds, then for seconds so it reads like the standard histograms on one board).
6. **Each call's total time is the standard `http.server.request.duration`,** in seconds, with the standard's labels. `http.route` is left out on purpose: for the proxy it is the vendor's route, and grows with apis.
7. **The warnings about dropped records stay** (once a minute per destination, and once at shutdown), so `kubectl logs` still shows a problem when the telemetry receiver is down or not set up. So do the reload warnings.
8. **The Go runtime numbers are sent,** from `go.opentelemetry.io/contrib/instrumentation/runtime` (Apache-2.0, the OpenTelemetry project).
9. **Every metric can be turned off on its own** with `telemetry.disable`: exact names, or a name ending in `*` for every name that starts with what is before it. A turned off metric is not measured or sent. A name that matches none of ours is a config warning, so a typo is seen.
10. **`telemetry` changes only on a restart,** like exporters; a change is logged as `telemetry changed and needs a restart` and sets `hotline.config.restart_needed`.
11. **Profile pages are off by default.** `telemetry.pprof: true` serves Go's profile pages (`/debug/pprof/`, `/debug/vars`) on the admin port, for finding why a proxy is slow, with a warning at start because the admin port has no password. It needs a restart to change. Details: [decisions/profile-pages.md](decisions/profile-pages.md).

Chosen without a question: the certificate expiry is a time, not "days left", so it does not change every day; telemetry is sent on the same timing as the exporters; when the telemetry receiver itself is down only the warning lines show it.
