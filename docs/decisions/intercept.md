# Intercept ingress decisions (Peter, 2026-11-08)

Full wording in [docs/proxy-intercept.md](../proxy-intercept.md).

1. A port of its own, `listen.intercept`, with `address`, `issuer` (a name from `issuers`), `integration` (default not-passed), `passthrough` and `ech.relay` (both empty by default).
2. The target is the site name in the handshake, else the original destination IP; port is the original port, else 443.
3. ECH is answered as the outer name with no ECH keys; the client retries without it. Only the Go client was tried (it retries); curl and Chrome are still to be tried.
4. Nothing unread is relayed except the `passthrough` and `ech.relay` lists.
5. New contract: optional `proxy.Options.OriginalDestination func(net.Conn) (netip.AddrPort, error)`, approved by Peter on 2026-11-08.
6. The real firewall redirect is tested by one `@e2e` scenario that is skipped where the test cannot change the firewall.
