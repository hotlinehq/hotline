# CONNECT ingress decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-connect.md](../proxy-connect.md). Why: a proxy deployed apart from the apps needs the hop to it encrypted, with no change in the app.

1. The issuer comes from the customer: `listen.connect: { cert, key }`, re-read on change (since 2026-10-31 through the shared config reload). Without it, `CONNECT` gets `501`.
2. Hotline builds nothing for apps to trust the issuer; the setup guide shows how per language.
3. Every host is intercepted; pinned hosts go in `NO_PROXY`. No blind tunnel.
4. The integration ID comes from `Proxy-Authorization` on the `CONNECT`, for the whole tunnel; one inside is ignored. `CONNECT` works on plain and TLS ports.
5. Host certificates: made on first use, memory only, 24 hours.
6. HTTP/2 and HTTP/1.1 inside the tunnel. `CONNECT` over HTTP/2 works too: the tunnel is that `CONNECT`'s own stream, so many tunnels share one connection (Peter, 2026-10-30).
7. Calls go to the `CONNECT` host and port; another host gets `421`.
8. One record per call, `hotline.ingress=connect`; `hotline.timing.app_tls_us` on the first call of each connection; an app that doesn't trust the issuer gets a minimal record with `tls_untrusted_by_app`.
9. `hotline.timing.app_tls_us` also on TLS from apps (changes TLS from apps decision 8).
10. The issuer is made only for Hotline; files that are not an issuer stop the start; over a year gets a warning.
11. Plain HTTP inside a tunnel is allowed, handled like a forward proxy `http` call.

Found while building: Go's client offers HTTP/2 to an `https` proxy address and then speaks HTTP/1.1, so the proxy sets up TLS with apps itself and picks the HTTP version from the app's first bytes.
