# Listener decisions (Peter, 2026-11-13)

Full wording in [docs/proxy-listeners.md](../proxy-listeners.md). Breaking changes were allowed: the old layout is not read any more.

1. Every port or socket file is a named listener under `listeners`, with an `address` (`tcp://host:port` or `unix://path`), a `protocol` (`http`, the default, or `tcp`) and parts: `tls`, `connect`, `transparent` (the old `listen.intercept`) and `capture`. A listener with no part is a forward proxy port. `transparent` and `capture` each need a listener of their own.
2. The old `listen` section and `issuers` are ignored with a warning. A config with no listeners is refused.
3. `issuers` is renamed `certs`: named cert and key pairs. The place that uses one says how, with `mode`: `serve` shows it as it is, `sign` treats it as a certificate authority and signs a certificate for each name. `tls` serves by default, like nginx and HAProxy; `connect`, `transparent` and `capture` always sign. A listener may name one `cert` for all its parts; a part's own `cert` wins.
4. A pair is checked against its mode where it is used, and the error names the listener. A pair nothing uses is only checked to match and not to have expired.
5. The admin port is a top-level `admin: { address, tls? }` section, never a listener, so apps never reach it by accident. Its `tls` works like a listener's. Client certificates come later.
6. `protocol: tcp` is only for transparent and capture listeners: it relays every byte untouched and reads nothing, so `cert`, `ports`, `passthrough` and `ech` are not settings there.
7. A reload changes certs, passthrough lists, ports and integration live. Adding, removing or moving a listener or the admin port, or changing a listener's protocol or parts, needs a restart: the old ports stay open as they started, the rest of the new config is used, and the log names the listener.
8. The capture port list is `capture.ports` (was `http`), `[80, 443]` by default.
9. `hotline.ingress=intercept` is renamed `transparent`. `hotline.certificate.use` is `<listener>.<part>`, such as `apps.tls` or `admin.tls`.
