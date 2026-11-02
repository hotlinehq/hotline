# Transparent redirect decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-transparent-redirect.md](../proxy-transparent-redirect.md).

1. The proxy doesn't care how the cluster redirects; the setup guide covers name lookup (`hostAliases`) and network rules in the pod; the tests fake name lookup.
2. The app adds `Proxy-Authorization` itself as a default header; no new way to pass the integration ID.
3. No separate port or record value: a redirected call is a Host header call, `hotline.ingress=host_header`.
4. The port is always kept, port 80 too (`Host: api.x.com:80` goes to `https` on port 80); no port means 443.
