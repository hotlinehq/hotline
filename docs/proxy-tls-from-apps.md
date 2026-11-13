# TLS from apps to the proxy: scope and test cases

*Agreed with Peter, 2026-10-28, and built. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo.*

## What the feature is

A call through Hotline uses two connections:

1. **App to proxy.** Today this is always plain, unencrypted HTTP.
2. **Proxy to vendor.** This is already encrypted. The proxy makes the `https` call to the vendor itself (TLS origination, built in slice 1).

This feature encrypts the first connection. The app opens a TLS connection (an encrypted connection, the same kind a browser opens to a website) to the proxy, and the proxy shows **its own** certificate (its digital ID card, carrying the proxy's own name, such as `hotline-proxy.hotline.svc`). Inside that encrypted connection the app sends the same request it sends today, with the full vendor address in the request line.

**Why it matters.** In the sidecar setup the app reaches the proxy on `localhost`, inside the same pod, so plain HTTP never crosses the network. In the shared setup it does: the vendor's API key in `Authorization`, and card or personal data in bodies, would cross the cluster network unencrypted. Customers under compliance rules such as PCI DSS (the card industry's security standard) need this hop encrypted. Peter decided on 2026-10-05 that it is optional but the proxy must support it.

**What it is not.**
- **Not CONNECT.** With CONNECT the app asks the proxy to open a blind tunnel and then talks TLS straight to the vendor, so the proxy sees nothing. Reading that would need certificates faked for each vendor on the fly (interception), which stays out of scope. CONNECT keeps getting `501`.
- **No vendor certificates.** The proxy never needs a certificate for `api.stripe.com`. Only its own.

## What the docs already say

- `proxy-spec.md`: `listen.tls: { cert, key }` is shown, commented out, as "optional TLS from apps".
- `egress-proxy-options.md`: matrix cases 4 to 6 and 9 to 10 are "TLS to the proxy". Case 4 (`https://` address) and case 5 (`http://` address, upgraded by TLS origination) are the forward proxy ones, so they belong to this slice. Cases 6, 9 and 10 need the Host header ingress.
- `end-to-end.feature`: the scenario "The client matrix over TLS to the proxy" (Go and Node, cases 4 and 5) is tagged `@later`, waiting for this slice.
- `research/absolute-form-tls-example`: a working Go demo of cases 4 and 5, including client certificates (mutual TLS).
- `memory.md`: this is the third item in "still to come"; slice 1 decision 14 deferred it.

## Requirements

1. **One port or two** (Peter, 2026-10-28). When the `tls` part is set, that listener speaks only TLS. A plain request sent to it fails. A customer who wants both writes two listeners (since 2026-11-13). Mixing both on one port means guessing from the first bytes, which is fragile.
2. **Where the certificate comes from** (Peter, 2026-10-28). Two files in PEM (the usual text format for certificates and keys), `cert` and `key`, the same way the vendor fingerprint keys are files. On Kubernetes they are a mounted Secret, typically written by cert-manager (the common Kubernetes tool that issues and renews certificates). Hotline never issues certificates itself. The `cert` file may hold the full chain (the proxy's certificate followed by the issuers in between).
3. **How apps trust the proxy** (Peter, 2026-10-28). Hotline builds nothing for this and never makes its own (self-signed) certificate. The certificate comes from the customer's own internal issuer (certificate authority), and each app trusts that issuer the usual way for its language. The certificate's name must match the address the app uses. We document this with an example per client (Go, Node, Java, Rust) but build nothing for it. The proxy has one certificate and serves it whatever name the app asks for.
4. **Renewing the certificate without a restart** (Peter, 2026-10-28). The proxy watches both files. When they change, it loads the new pair; new connections get the new certificate and open ones keep the old. A broken or mismatched pair is ignored: the old one stays and the proxy logs why. This matches how the fingerprint key files already work. cert-manager usually renews every 60 days or so, so a restart for each renewal would be a nuisance.
5. **Client certificates (mutual TLS, where the proxy also checks a certificate from the app)** (Peter, 2026-10-28). Not in this slice. It belongs with caller authentication, which the spec puts in "later". The demo shows it is easy to add (`clientCA` file) when we get there. Peter said on 2026-10-05 it must be supported eventually, so this only orders the work.
6. **TLS versions and ciphers** (Peter, 2026-10-28). TLS 1.2 and 1.3 only, with Go's safe defaults for ciphers. No config options for them.
7. **HTTP/2 over TLS** (Peter, 2026-10-28). In this slice the proxy offers only HTTP/1.1 during the handshake, so clients fall back to it and every call works. Over HTTP/2 there is no full address in a request line, so those calls would need the Host header ingress, which is not built yet. HTTP/2 over TLS is switched on together with the Host header ingress (built 2026-10-29, see `proxy-host-header.md`).
8. **What the record says** (Peter, 2026-10-28). No new record field. The port is all TLS or all plain, so a field on each call would repeat one fact. Whether a proxy uses TLS is in its config; the proxy's own telemetry no longer repeats it (Peter, 2026-11-01, [proxy-telemetry.md](proxy-telemetry.md)). Changed 2026-10-29: how long the TLS setup took is recorded, as `hotline.timing.app_tls_us` on the first call of each connection (see [proxy-connect.md](proxy-connect.md)). Changed 2026-10-31: it is the `app tls handshake` span of that call's trace instead, inside the call span ([proxy-traces.md](proxy-traces.md)).
9. **Errors** (Peter, 2026-10-28).
   - Certificate or key missing, unreadable, not matching each other, or already expired at start: the proxy refuses to start and exits with 1, and the log line says why. `validate` reports the same. Starting while "not ready", falling back to plain HTTP, and making our own certificate were all ruled out.
   - A failed handshake from an app (it doesn't trust the proxy, sends plain HTTP, or uses an old TLS version): no request was read, so there is no record and no `Proxy-Status`. A debug log line, and a count in `hotline.app_tls.failures` ([proxy-telemetry.md](proxy-telemetry.md)). Go answers a plain HTTP request on a TLS port with a short `400` by itself; we keep that.
10. **Readiness.** Settled by 9: the proxy never runs without a valid certificate, and a broken renewal keeps the old one, so `/readyz` needs nothing extra for TLS.
11. **Certificate expiry warning** (Peter, 2026-10-28). In the proxy's own telemetry, as `hotline.certificate.not_after` ([proxy-telemetry.md](proxy-telemetry.md)). Not a record field, since it is not about a call.

## Tests

- Feature scenarios: `src/hotline-proxy/features/tls-from-apps.feature`. A call over TLS gets through; the proxy shows its one certificate whatever name is asked for; a plain request on the TLS port gets `400`; an app that doesn't trust the proxy, or allows only TLS 1.1, fails the setup and the proxy logs it at debug level; TLS 1.2 and 1.3 both work; a bad certificate stops the start; renewing the files changes the certificate for new connections only; a broken renewal keeps the old one.
- End to end: `end-to-end-tls.feature` runs the Go client's cases 4 and 5 against the built program. The Node rows wait, tagged `@later`, for the Node client. `program.feature` checks that `validate` refuses a missing key file.

## How it is built

- The `tls` part is read in `internal/config`, which also checks the pair and its expiry. The same check runs at every renewal.
- The proxy shows its certificate through `GetCertificate`. On each new app connection it looks at the two files (two quick file lookups) and loads them again only when either is a new file or was written since. A Kubernetes Secret update swaps both files at once, which this sees.
- A small wrapper on each new connection reads the first byte: TLS always starts with `0x16`. Anything else is answered `400` with `Proxy-Status: hotline; error=http_request_error` and closed. Go's own check only catches requests whose line starts with `GET /` and the like, not the full addresses of the forward proxy ingress.
- An app gets 30 seconds to set up TLS, so a connection that never finishes the setup cannot stay open forever. Not asked for in the requirements; added because Go's server has no such limit on its own.
- The HTTP server's line about a failed TLS setup is logged at debug level as `app TLS setup failed`; its other lines stay warnings.

## Not in this slice

CONNECT and interception (built later, see [proxy-connect.md](proxy-connect.md)); client certificates; a record field saying the app used TLS; the certificate expiry warning; a second listener for plain and TLS at once; certificates from a secrets manager.
