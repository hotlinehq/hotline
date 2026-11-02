# TLS from apps decisions (Peter, 2026-10-28)

Full wording in [docs/proxy-tls-from-apps.md](../proxy-tls-from-apps.md).

1. With `listen.tls` set, the proxy port accepts only TLS.
2. Certificate and key are PEM files; on Kubernetes a Secret, usually from cert-manager.
3. Hotline builds nothing for apps to trust the proxy and never makes its own certificate; customers use their own issuer.
4. The files are re-read when they change; a broken new pair keeps the old one. Since 2026-10-31 through the shared config reload (file check every 5 seconds, or SIGHUP).
5. Client certificates (mutual TLS) come later, with caller authentication.
6. TLS 1.2 and 1.3 only, Go's default ciphers, nothing to configure.
7. Only HTTP/1.1 over TLS for now; HTTP/2 comes with the Host header ingress.
8. No record field for it; the proxy's own health data says it once. Changed 2026-10-29: the TLS setup time is recorded as `hotline.timing.app_tls_us`.
9. A missing, broken, mismatched or expired certificate stops the proxy at start. A failed app TLS setup is a debug log line and, later, a count in health data.
10. The warning before the proxy's own certificate expires comes with the health data.
