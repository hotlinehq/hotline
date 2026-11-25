# Profile pages decisions (Peter, 2026-11-07)

*Since [listeners.md](listeners.md) (2026-11-13), every `listen.*` section is a named listener under `listeners`, `listen.admin` is the top-level `admin` section, and `issuers` is `certs`; the old keys are only warned about. The names below are as they were when decided.*

Full wording in [features/profile-pages.feature](../../src/hotline-proxy/features/profile-pages.feature).

1. `telemetry.pprof: true` serves Go's profile pages on the admin port: `/debug/pprof/` (processor, memory, goroutines and so on) and `/debug/vars` (the exact total of bytes handed out). No section of its own: it sits in the existing `telemetry` section, and works without a `telemetry.otlp` exporter.
2. Off by default. The admin port has no password and a processor profile makes the proxy work extra, so the proxy logs one warning at start when it is on.
3. On without `listen.admin` is a config warning, and there are no pages.
4. Changing it needs a restart (`telemetry.pprof` shows in the restart-needed warning).
5. The load test turns it on by writing it into the config it generates. The old `-tags loadprofile` build and `HOTLINE_LOAD_PPROF_ADDR` are gone.
6. Sending profiles to a profile store (Pyroscope) is not part of the proxy. OpenTelemetry has no stable profiles signal in the Go SDK yet, no vendor library and no Grafana Alloy in the product: OpenTelemetry stays the facade, and a Collector (contrib) is the only middle piece if one is needed. The load test may upload its saved files by itself later.

## Tripwires (2026-11-25)

- Confirmation: the profile pages are served only when `telemetry.pprof` is on, and the proxy then logs one warning (`serveAdmin` in `proxy.go`).
- Revisit if the admin port gets a password or client certificates, or the OpenTelemetry Go SDK gets a stable profiles signal.
