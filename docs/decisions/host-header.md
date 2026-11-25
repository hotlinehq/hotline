# Host header ingress decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-host-header.md](../proxy-host-header.md).

1. Calls without a known integration (`not-passed`) go out over `https`, since a Host header call names no scheme.
2. Plain HTTP/2 (h2c) by prior knowledge only; no `Upgrade: h2c` handshake.
3. Guarded from the start by the internal address guard; no separate loop check.
4. No HTTP/3 for now; added later only if a real app needs it.

## Tripwires (2026-11-25)

- Confirmation: the proxy answers no `Upgrade: h2c` request with a switch to HTTP/2.
- Revisit if a real app needs HTTP/3 (see the HTTP/3 decisions in the project files) or the `Upgrade: h2c` handshake.
