# Listeners

Every port or socket file the proxy opens is a **listener**, written under `listeners` with a name of your choice (Peter, 2026-11-13). A listener has:

- `address`: `tcp://host:port` for a port, or `unix://path` for a socket file (a file on disk that programs on the same machine connect to). A socket path is absolute or starts with `~/`, and is at most 103 bytes.
- `protocol`: `http` (the default) reads the connections as calls; `tcp` relays the bytes untouched and reads nothing. Only transparent and capture listeners may use `tcp`.
- Parts, each optional:
  - `tls`: the listener takes only TLS.
  - `connect`: apps may open tunnels with CONNECT.
  - `transparent`: connections a firewall sent here, which carry no CONNECT (the old `listen.intercept`).
  - `capture`: the socket file the macOS capture app hands connections to (the old `listen.capture`).

A listener with no part is a forward proxy port. `transparent` and `capture` each need a listener of their own. The admin port (health checks and profile pages) is its own `admin` section, never a listener, so apps never reach it by accident.

## Certificates

Certificates live under `certs`, each a named cert and key pair. The place that uses one says how, with `mode`:

- `serve` shows the certificate as it is. This is the default for `tls`, as in nginx or HAProxy.
- `sign` treats the pair as a certificate authority (a certificate that may sign others), and signs a new certificate for each name an app calls. `connect`, `transparent` and `capture` always sign.

A listener may name one `cert` for all its parts; a part's own `cert` wins. Whether a pair fits its mode is checked where it is used: a certificate authority on `tls` without `mode: sign`, or an ordinary certificate where the proxy signs, stops the start and names the listener. A pair nothing uses is only checked to match and not to have expired.

## A full example

```yaml
admin:
  address: tcp://127.0.0.1:8081
  tls: { cert: internal }            # optional; plain HTTP without it

certs:
  shop:     { cert: ./ca/shop.pem, key: ./ca/shop-key.pem }          # a certificate authority
  capture:  { cert: ./ca/capture.pem, key: ./ca/capture-key.pem }    # the Mac app's authority
  internal: { cert: ./certs/hotline-internal.pem, key: ./certs/hotline-internal-key.pem }  # a company certificate

listeners:
  laptop:                            # one cert for every part
    address: tcp://127.0.0.1:8443
    cert: shop
    tls: { mode: sign }              # signs a certificate for the name the app calls
    connect: { passthrough: [accounts.example.com] }

  team:                              # parts with their own certs
    address: tcp://0.0.0.0:443
    tls: { cert: internal }          # shows the company certificate
    connect: { cert: shop }

  sidecar:                           # what the firewall sends here
    address: tcp://0.0.0.0:15001
    transparent: { cert: shop, integration: checkout, passthrough: ["*.example.org"], ech: { relay: [outer.example.net] } }

  sidecar-db:                        # relayed untouched to where the program called
    address: tcp://0.0.0.0:15002
    protocol: tcp
    transparent: { integration: checkout }

  mac-calls:                         # the Mac capture app's socket file
    address: unix://~/Library/Application Support/Hotline/calls.sock
    capture: { cert: capture, ports: [80, 443, 8080], passthrough: [pinned.example.com] }

apis:
  shop:
    hosts: [shop.example.com]
    spec: ./specs/shop.yaml

exporters:
  - console:
```

A part written with nothing after it, such as `connect:`, is on with its defaults.

## Capture ports

`capture.ports` lists the ports read as calls: TLS, or else plain HTTP. It is `[80, 443]` when left out, and `[]` reads none. Every other port is relayed untouched, at once, so a server that speaks first, such as SSH, just works. On a read port, bytes that are neither TLS nor HTTP are closed and recorded with `error.type=not_http`. With `protocol: tcp` on the listener, every port is relayed and `ports` and `cert` are not settings.

## Records and numbers

Calls on a transparent listener say `hotline.ingress=transparent`. A connection relayed because of `protocol: tcp`, or a capture port that is not read, says `hotline.intercept.mode=passthrough` and `hotline.intercept.reason=tcp`. The metric `hotline.certificate.not_after` names each certificate by listener and part, such as `laptop.tls`, `team.connect`, `sidecar.transparent`, `mac-calls.capture`, or `admin.tls`.

## Reload

A reload changes a listener's certs, passthrough lists, ports and integration for new connections. Adding, removing or moving a listener, or changing its protocol or which parts it has, needs a restart: the old ports stay open as they started, the rest of the new config is used at once, and the log names the listener. The same holds for the admin port.

## The old layout

The old `listen` section and `issuers` are not read any more; each gives a warning, and a config with no `listeners` is refused. Move them like this:

| Old | New |
| --- | --- |
| `listen.proxy: ":8080"` | a listener `apps: { address: tcp://:8080 }` |
| `listen.admin: ":8081"` | `admin: { address: tcp://:8081 }` |
| `listen.tls: { cert, key }` | a `certs` entry, and `tls: { cert: name }` on the listener |
| `listen.connect: { issuer }` | `connect: { cert: name }` on the listener |
| `listen.intercept: { address, issuer, ... }` | a listener with `transparent: { cert: name, ... }` |
| `listen.capture: { path, issuer, http }` | a listener `unix://path` with `capture: { cert: name, ports }` |
| `issuers:` | `certs:` |

Scenarios: `src/hotline-proxy/features/listeners.feature`. Decisions: [docs/decisions/listeners.md](decisions/listeners.md).
