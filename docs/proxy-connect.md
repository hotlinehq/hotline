# The CONNECT ingress, with interception: scope, setup and tests

*Agreed with Peter and built, 2026-10-29. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo.*

## What the feature is

Plain HTTP from the app to the proxy can be fine for a sidecar, but when the proxy runs as its own deployment, the hop to it crosses the network and must be encrypted (Peter, 2026-10-29). With this ingress the app keeps its normal `https` address and only sets the proxy, for example `HTTPS_PROXY`. Its client then sends `CONNECT api.stripe.com:443`, a request that asks the proxy to open a tunnel.

The proxy answers `200` and then sets up TLS with the app inside the tunnel itself, posing as the vendor. It shows a certificate for `api.stripe.com` that it makes on the fly, signed by an issuer (a certificate authority: a certificate and key allowed to sign other certificates) that the app trusts. This is called interception. The proxy can then read each call in the tunnel, handles it like a call on any other ingress, and opens its own TLS connection to the real vendor.

Gains: no change in the app's code, the hop to the proxy is encrypted, and signed calls (AWS) need no `https` to `http` rewrite, since the app signs exactly what is sent.

## Decisions (Peter, 2026-10-29)

1. **The issuer comes from the customer**: two PEM files, `cert` and `key`, written once in the top level `issuers` section under a name, and picked by `listen.connect: { issuer: name }` (Peter, 2026-11-08; the earlier `listen.connect: { cert, key }` form is removed, and a config that uses it stops the start). On Kubernetes a mounted Secret, which cert-manager can make and renew. The proxy picks up renewed files through the config reload (see [proxy-config-reload.md](proxy-config-reload.md)). It never invents its own issuer. A name not in `issuers`, or files that are not an issuer, also stop the start. Without `listen.connect`, `CONNECT` keeps getting `501`.
2. **Hotline builds nothing for apps to trust the issuer.** The setup guide below shows how per language.
3. **Every host is intercepted**, as on the other ingresses. An app that pins a vendor's certificate leaves that host out with `NO_PROXY`. No blind tunnel.
4. **The integration ID comes from the `CONNECT` request**, in `Proxy-Authorization`, and covers every call in the tunnel. A `Proxy-Authorization` inside the tunnel is removed and ignored. `CONNECT` works on a plain port and on a TLS port (`listen.tls`), where the ID travels encrypted too.
5. **Certificates per host** are made on the first tunnel to a host, kept in memory only, valid for 24 hours, and made again when they near their end or the issuer changes.
6. **HTTP/2 and HTTP/1.1 inside the tunnel**; the app picks. The `CONNECT` itself can come over HTTP/1.1 or HTTP/2. Over HTTP/2 the tunnel is that `CONNECT`'s own stream (one of many lanes in a shared connection), so one connection to the proxy can carry many tunnels (Peter, 2026-10-30).
7. **Calls go to the `CONNECT` host and port.** A call for another host gets `421` and a minimal record.
8. **Records.** One per call in the tunnel, `hotline.ingress=connect`. No record for a `CONNECT` that opens fine; a minimal one for a refused `CONNECT`. A new timing, `hotline.timing.app_tls_us`, holds the proxy's TLS setup with the app, on the first call of each connection, so all of the proxy's own cost is visible. An app that doesn't trust the issuer gets a minimal record with `error.type=tls_untrusted_by_app`.
9. **The same timing on TLS from apps**: `hotline.timing.app_tls_us` also goes on the first call of each connection to a `listen.tls` port. This changes TLS from apps decision 8. Since 2026-10-31 it is the `app tls handshake` span of the call's trace ([proxy-traces.md](proxy-traces.md)).
10. **Keeping the signing key safe.** The issuer is made only for Hotline and trusted only by the apps that use the proxy. At start the proxy refuses files that are not an issuer, and warns when the issuer is valid for more than a year. The key is never logged or copied.
11. **Plain HTTP inside a tunnel is allowed**, told from TLS by the first byte, and handled like a plain `http` call on the forward proxy ingress, with TLS origination.

Found while building: Go's client offers HTTP/2 when it sets up TLS to an `https` proxy address, then sends `CONNECT` over HTTP/1.1 anyway. So the proxy now sets up TLS with apps itself, on its TLS port too, and tells HTTP/1.1 from HTTP/2 by the first bytes the app sends, not by what it offered.

## Setup guide

### 1. Make an issuer for Hotline only

With cert-manager, a self-signed issuer that makes one issuer certificate, renewed every 90 days:

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata: { name: hotline-bootstrap, namespace: hotline }
spec: { selfSigned: {} }
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: { name: hotline-interception, namespace: hotline }
spec:
  isCA: true
  commonName: hotline interception issuer
  secretName: hotline-interception
  duration: 2160h      # 90 days; over a year gets a warning
  renewBefore: 720h
  privateKey: { algorithm: ECDSA, size: 256 }
  issuerRef: { name: hotline-bootstrap, kind: Issuer }
```

Mount the Secret in the proxy and point the config at it:

```yaml
listen:
  proxy: ":8443"
  tls: { cert: /etc/hotline/tls/tls.crt, key: /etc/hotline/tls/tls.key }      # optional: encrypts the CONNECT itself
  connect: { issuer: customer }
issuers:
  customer: { cert: /etc/hotline/ca/tls.crt, key: /etc/hotline/ca/tls.key }
```

Never use the company's main issuer here: whoever holds this key can pose as any website to the apps that trust it.

### 2. Let the apps trust it

Give the apps the issuer certificate (`ca.crt`, never the key), for example with trust-manager, which copies it into a ConfigMap in each app namespace, together with the public issuers if the app's setting replaces the system list. When the issuer is renewed, apps should trust the old and the new one for a while.

- **Go:** `SSL_CERT_FILE=/etc/hotline-trust/bundle.pem` (this replaces the system list, so the bundle must hold the public issuers too), or add it to `tls.Config.RootCAs`.
- **Node:** `NODE_EXTRA_CA_CERTS=/etc/hotline-trust/ca.crt` (added to the built-in list).
- **Java:** import it into a truststore and start with `-Djavax.net.ssl.trustStore=…`.
- **Rust (reqwest):** `ClientBuilder::add_root_certificate`, or the system list with native TLS.
- **Python (requests):** `REQUESTS_CA_BUNDLE=/etc/hotline-trust/bundle.pem`.

### 3. Point the app at the proxy

`HTTPS_PROXY=http://shop-a:x@hotline.hotline.svc:8080`, or `https://…:8443` when the proxy port uses TLS. The user name is the integration ID. Nothing else changes in the app.

### Watch out

- **Java's built-in clients, as far as we know, send proxy credentials only after a `407` challenge**, and turn off Basic for tunnels by default (`jdk.http.auth.tunneling.disabledSchemes`). Since the proxy never asks, they would call without an ID, as `not-passed`. OkHttp sends them up front through its proxy authenticator. To be confirmed when the Java clients join the end-to-end tests.
- **Certificate pinning**: an app that accepts only the vendor's exact certificate fails; list that host in `NO_PROXY`.
- **`NO_PROXY`** lists hosts that skip the proxy; check it does not hold the vendors you want measured.
- An `https` proxy address is not supported by every client (Java's built-in clients, for one); those use a plain proxy address, ideally with the proxy as a sidecar.

## Tests

- `src/hotline-proxy/features/connect.feature`: a call through a tunnel, a signed call, the certificate shown, reuse, every host, the ID from `CONNECT`, the wrong host (`421`), HTTP/1.1 and HTTP/2 inside, plain HTTP inside, the handshake timing, an app that doesn't trust the issuer, the internal address guard, `CONNECT` on a TLS port, refused `CONNECT`s, `CONNECT` over HTTP/2 (one tunnel, many tunnels on one connection, plain HTTP inside, refused), stopping with a tunnel open (over HTTP/1.1 and HTTP/2), renewing the issuer, a bad issuer, and the long-lived issuer warning.
- `tls-from-apps.feature`: the handshake timing on the first call of each connection.
- `end-to-end-connect.feature`: the real program with Go's own client and the standard proxy setting, through a plain and an https proxy address.

## How it is built

- `connect.go`: the `CONNECT` handler (answers `200`, takes over the connection over HTTP/1.1 or carries the tunnel in its stream over HTTP/2, sets up TLS inside the tunnel, records a failed setup), how a call in a tunnel finds its vendor, and the interceptor that makes and keeps the per host certificates.
- `appconn.go`: the app connection as the HTTP server sees it, with the handshake timing, and the listener that hands tunnels to a second HTTP server for their calls.
- `appcert.go`: one type that follows a certificate and key in files, for `listen.tls` and `listen.connect`. On a TLS port the proxy now sets up TLS itself, on the server's own goroutine for the connection.
- `internal/config`: the `issuers` section and `listen.connect`, checked by `LoadIssuer`.
