# Profile pages decisions (Peter, 2026-11-07)

Full wording in [features/profile-pages.feature](../../src/hotline-proxy/features/profile-pages.feature).

1. `telemetry.pprof: true` serves Go's profile pages on the admin port: `/debug/pprof/` (processor, memory, goroutines and so on) and `/debug/vars` (the exact total of bytes handed out). No section of its own: it sits in the existing `telemetry` section, and works without a `telemetry.otlp` destination.
2. Off by default. The admin port has no password and a processor profile makes the proxy work extra, so the proxy logs one warning at start when it is on.
3. On without `listen.admin` is a config warning, and there are no pages.
4. Changing it needs a restart (`telemetry.pprof` shows in the restart-needed warning).
5. The load test turns it on by writing it into the config it generates. The old `-tags loadprofile` build and `HOTLINE_LOAD_PPROF_ADDR` are gone.
6. Sending profiles to a profile store (Pyroscope) is not part of the proxy. OpenTelemetry has no stable profiles signal in the Go SDK yet, no vendor library and no Grafana Alloy in the product: OpenTelemetry stays the facade, and a Collector (contrib) is the only middle piece if one is needed. The load test may upload its saved files by itself later.
