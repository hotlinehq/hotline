# Config reload decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-config-reload.md](../proxy-config-reload.md).

1. One mechanism for every file, with two triggers: a check every 5 seconds (identity, time and size; no full read) and SIGHUP for "now". Kubernetes sends no signal when a mounted file changes.
2. Certificates (`listen.tls`, `listen.connect`) use the same check, replacing the check on each new connection.
3. Watched: exactly the files the last load read or tried to read; a missing overlay is watched until it arrives.
4. Live: integrations, apis, specs, overlays, keys, trace settings, certificates. Restart: listen addresses, adding or removing listen.tls or listen.connect, exporters (Envoy's split).
5. `hotline.config.version` = config file name + `+` + first 12 hex of a SHA-256 over the config, specs and overlays (not keys or certificates).
6. A failed reload is a warning in the log, and counted in the proxy's own telemetry (since 2026-10-31).
