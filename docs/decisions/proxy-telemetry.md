# Proxy telemetry decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-telemetry.md](../proxy-telemetry.md).

1. Metrics only, to `telemetry` only. No `telemetry` section: nothing measured, one warning at start.
2. No label grows with traffic: never by integration, vendor, host, URL or route.
3. Standard names where they exist, even "development" ones: `otel.sdk.*` for queues and sends, `http.server.active_requests`, `http.server.request.duration` (seconds, standard labels, no `http.route`). Our own: `hotline.proxy.overhead` (seconds, by ingress, 8 fixed limits from 0.00005 to 0.1).
4. State as 1 or 0 gauges: `hotline.destination.state`, `hotline.config.reload.last_success`, `hotline.config.restart_needed`; plus `hotline.config.reloads` and `hotline.config.info`.
5. Certificates: `hotline.certificate.not_after`, a time; failed app TLS setups: `hotline.app_tls.failures`.
6. The Go runtime numbers, from the OpenTelemetry contrib module.
7. Every metric can be turned off with `telemetry.disable` (exact name or ending in `*`); an unknown name is a warning.
8. The dropped records and reload warnings stay in the log.

Reworked with Peter on 2026-11-01, from 27 metrics to 14: no unit on 1 or 0 gauges (no `_ratio`); `hotline.exporter.up`, `.queue.size`, `.sent`, `.dropped` replace `otel.sdk.*` and `hotline.destination.state`; config is only `hotline.config.reload_failed` and `hotline.config.restart_needed`; `hotline.proxy.overhead` and the info metrics are gone; four Go numbers; every metric has a description and a unit Grafana can chart. Points 3 and 4 above are replaced by this.
