# Host header ingress decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-host-header.md](../proxy-host-header.md).

1. Calls without a known integration (`not-passed`) go out over `https`, since a Host header call names no scheme.
2. Plain HTTP/2 (h2c) by prior knowledge only; no `Upgrade: h2c` handshake.
3. Guarded from the start by the internal address guard; no separate loop check.
4. No HTTP/3 for now; added later only if a real app needs it.
