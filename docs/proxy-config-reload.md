# Config reload without a restart

Agreed with Peter on 2026-10-31. Tests: `src/hotline-proxy/features/reload.feature`, the renewal scenarios in `tls-from-apps.feature` and `connect.feature`, and `end-to-end-reload.feature` (the program, with SIGHUP and the timed check).

## What it does

The proxy checks its files every 5 seconds, and a HUP signal (`kill -HUP <pid>`) reloads at once. Either one reads everything again: the config, the specs, the overlays, the key files and the `listen.tls` and `listen.connect` certificates. The new config is checked the same way as at start and by `validate`, and is used only if all of it is valid. New calls use it; calls in progress finish on the config they started with. A broken config is not used: the old one stays, and each problem is logged as a warning (`config reload failed, the old config stays in use`). A good reload logs `config reloaded` with the new version.

## Decisions (Peter, 2026-10-31)

1. **One mechanism, two triggers.** The file check every 5 seconds, so it works on Kubernetes with nothing extra (Kubernetes sends no signal when a mounted Secret or ConfigMap changes), and HUP for "reload now", as HAProxy and Squid do.
2. **Certificates use the same check.** This replaces the check on each new connection from TLS from apps (decision 4) and CONNECT (decision 1). A renewed certificate is taken within 5 seconds; a broken one fails the reload and the old one stays.
3. **What is watched** is exactly what the last load read or tried to read. Specs may only point to schemas in their own file, so there are no hidden files. An overlay added by a new config joins the list; a missing file is watched until it arrives. After a failed reload the files of the config in use stay watched too.
4. **How a file is checked:** its identity on disk, its time and its size. It is read in full only when one differs. Kubernetes swaps a mounted file for a new one, which is always a different file.
5. **Files that arrive out of order** (config and overlays in different ConfigMaps) fail once with a warning; the proxy tries again when the missing file arrives. Keeping the config and its overlays in one ConfigMap avoids this, as Kubernetes swaps all of its files in one step.
6. **What a reload may change,** the way Envoy splits its config. Live: integrations, apis, specs, overlays, keys, trace settings, certificates and `specChecks.queueMemory`. Needs a restart: `listen.proxy`, `listen.admin`, adding or removing `listen.tls` or `listen.connect`, and exporters. A change to those is logged as `<setting> changed and needs a restart` and the rest switches.
7. **The version on records** is the config file's name (without its folder), a `+`, and a fingerprint: the first 12 hex characters of a SHA-256 of the config, specs and overlays (each path relative to the config's folder, in path order). Key and certificate files are left out, so a renewal is not a new config and no secret goes into it. Example: `hotline-proxy.aws-shop.yaml+a3f9c21e07b4`. The config has no `version` field of its own: the fingerprint changes with every edit (Peter, 2026-11-01). An old config that still has one loads with a warning that the field is ignored.
8. **A failed reload is a warning in the log, and shown** in the proxy's own telemetry: `hotline.config.reload_failed` until a good reload, and `hotline.config.restart_needed` while a change waits for a restart (reworked 2026-11-01). Detail: [proxy-telemetry.md](proxy-telemetry.md).
9. A key read from an environment variable changes only on a restart.
