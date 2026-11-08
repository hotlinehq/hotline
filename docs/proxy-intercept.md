# The intercept ingress

For programs that cannot be told about a proxy. A firewall on the same machine sends their secure connections to the proxy's `listen.intercept` port, and the program never knows. The spec is `proxy-spec.md` in `hotline-strategic` (Peter, 2026-11-08).

## Config

```yaml
listen:
  intercept:
    address: 127.0.0.1:8444        # a port of its own: raw TLS cannot share the proxy port
    issuer: customer               # a name from the issuers section
    integration: ci-lint           # the ID of every call on this port; not-passed if left out
    passthrough: [pinned.example.com, "*.internal.example.com"]   # relayed untouched, empty by default
    ech: { relay: [outer.example.net] }                            # ECH outer names relayed untouched, empty by default
issuers:
  customer: { cert: ./ca/cert.pem, key: ./ca/key.pem }
```

A missing address, a missing issuer or an issuer name that is not in `issuers` stops the start. Adding or moving the listener needs a restart; a renewed issuer is picked up by the config reload.

## How a connection is handled

1. The proxy reads the first message of the TLS handshake without answering it. It finds the site name (the SNI) and whether the handshake carries encrypted client hello (ECH, where the real name is hidden and only an outer name shows).
2. Not a TLS handshake: closed, with a minimal record `error.type=not_tls`.
3. ECH with an outer name in `ech.relay`: relayed untouched. Other ECH: the proxy answers as the outer name and offers no ECH keys, so a client that follows the ECH standard retries without it. The first try gets a minimal record `error.type=ech_rejected`.
4. A site name in `passthrough`: relayed untouched, dialed by name (never by the address the program chose), through the internal address guard. The record is minimal with `hotline.intercept.mode=passthrough` and `hotline.intercept.reason` (`passthrough` or `ech`), written when the relay ends.
5. No site name: the host is the original destination IP. If it cannot be read, closed with `error.type=no_sni`.
6. Otherwise the proxy shows a certificate for the host, signed by the named issuer, and hands the connection to the same server as a CONNECT tunnel. Each call is `hotline.ingress=intercept`; a `Host` for another host gets 421.

The target port is the original port when the proxy can read it, else 443.

## The original destination

The address and port a redirected connection was first sent to sit at the TCP level, below TLS, so the handshake cannot tell them. Linux keeps them in the connection's record. `proxy.Options.OriginalDestination` (optional, `func(net.Conn) (netip.AddrPort, error)`) reads them: the program fills it for IPv4 on Linux, tests pass a fake. Left empty, calls with no site name are refused.

## Without a firewall

The port is an ordinary TCP port that expects TLS, so a program can also be sent there by DNS or a hosts file entry. Then the original port and calls by bare IP do not work.

## Not built here

The firewall rules (the GitHub Action and the setup guide do that), IPv6 original destinations, and a certificate expiry gauge for this issuer.
