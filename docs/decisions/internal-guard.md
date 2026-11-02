# Internal address guard decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-internal-guard.md](../proxy-internal-guard.md).

1. Guard against server side request forgery: after DNS, the proxy refuses to connect to internal IPs (loopback, link-local with the cloud metadata address, private, shared, unspecified, multicast), on every ingress.
2. An api can set `allowInternalIPs: true`; so can the `not-passed` entry. Calls without an ID still reach public hosts.
3. Its own slice, before the Host header ingress. It also stops calls to the proxy itself, so there is no separate loop check.
