# Internal address guard decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-internal-guard.md](../proxy-internal-guard.md).

1. Guard against server side request forgery: after DNS, the proxy refuses to connect to internal IPs (loopback, link-local with the cloud metadata address, private, shared, unspecified, multicast), on every ingress.
2. An api can set `allowInternalIPs: true`; so can the `not-passed` entry. Calls without an ID still reach public hosts.
3. Its own slice, before the Host header ingress. It also stops calls to the proxy itself, so there is no separate loop check.

## Tripwires (2026-11-25)

- Confirmation: every connection to a vendor is opened by `dialer.dial` in `dial.go`, which drops internal IPs. The only other dialers in `src/hotline-proxy` are the `net.Dialer` that `cmd/main.go` hands to it and the check in `capture.go` of the proxy's own socket file.
- Revisit if an api needs only some internal addresses allowed, such as one private range, rather than all of them or none.
