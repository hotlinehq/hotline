# Slice 1 decisions (Peter, 2026-10-28)

Full wording is at the end of [docs/proxy-slice-1.md](../proxy-slice-1.md).

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

Export queues, from the load test (Peter, 2026-11-09): a full queue keeps what waits and refuses what is new, for records and spans alike, replacing the drop of the oldest in point 13. A call's spans go into the span queue together or not at all, so the queue never splits a trace; a call with more spans than the whole queue holds is dropped. Both queues become fixed-size rings that never grow. `queueSize` still counts records and spans, and `hotline.exporter.dropped` still counts spans. Built: `features/export.feature` keeps it so.
