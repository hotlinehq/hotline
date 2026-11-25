# Health check decisions (Peter, 2026-10-31)

*Since [listeners.md](listeners.md) (2026-11-13), every `listen.*` section is a named listener under `listeners`, `listen.admin` is the top-level `admin` section, and `issuers` is `certs`; the old keys are only warned about. The names below are as they were when decided.*

Full wording in [docs/proxy-health-checks.md](../proxy-health-checks.md).

1. `/readyz` says no (503) only before the first config has loaded, while the proxy port is not listening, and from the first stop signal on. Never because a reload failed or an exporter is down: one bad shared config edit would otherwise take every copy out of service.
2. `/livez` says no only when the proxy is stuck.
3. After the stop signal the proxy still takes new calls for `--drain-delay` (5 seconds, inside the 25 second stop limit), since Kubernetes needs a moment to stop sending calls and our image has no shell for `sleep`. A second signal skips it.
4. No `listen.admin` means no health checks and one warning; the proxy still runs.
5. Plain HTTP, only `/livez` and `/readyz`, no records. No detail page: the state of each part comes with the proxy's own telemetry, a chapter of its own.
6. The end-to-end tests pass `--drain-delay 0s` unless a scenario sets it, so they stop at once.

## Tripwires (2026-11-25)

- Confirmation: `/readyz` in `proxy.go` looks only at whether the proxy is stopping, never at reloads or exporters.
- Revisit if a deployment needs a copy taken out of service when its exporter is down, or Kubernetes needs more than the 5 second drain delay.
