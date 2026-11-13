# The transparent redirect ingress: scope, setup and tests

*Agreed with Peter and built, 2026-10-29. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo.*

## What the feature is

An ingress is a way an app reaches the proxy. With this one, the app keeps the vendor's own name and only turns `https` into `http` in its address (`http://api.stripe.com`). Something in the cluster quietly sends that plain call to the proxy instead of the vendor. The proxy then calls the vendor over `https` (TLS origination: the proxy starts the encryption itself).

## What each part receives

App calls `http://api.stripe.com/v1/payment_intents`; the proxy's Service is at `10.0.0.5`.

- **Name lookup.** The app asks for `api.stripe.com` and gets `10.0.0.5`. It connects there on port 80.
- **Network rules.** The app gets Stripe's real address, connects to it on port 80, and a rule in the app's pod changes the target to the proxy.
- **Either way the proxy receives:**
  ```http
  POST /v1/payment_intents HTTP/1.1
  Host: api.stripe.com
  Proxy-Authorization: Basic c2hvcC1hOng=
  ```
- **The proxy** looks up `api.stripe.com` itself and sends the same request to Stripe over `https` on port 443, without `Proxy-Authorization`.

## Requirements

1. **How the cluster redirects: the proxy doesn't care** (Peter, 2026-10-29). It reads the vendor from `Host` either way. The setup guide describes both name lookup and network rules; the end-to-end test fakes name lookup.
2. **The app adds the integration ID** in `Proxy-Authorization`, as a default header, as on the other ingresses (Peter, 2026-10-29). Without it the call is `not-passed`.
3. **No separate ingress inside the proxy** (Peter, 2026-10-29). Redirected calls arrive on the normal proxy port and are Host header calls: same rules, same record, `hotline.ingress=host_header`. No new port, no new record value.
4. **The port is always kept** (Peter, 2026-10-29; slice 1 decision 4 unchanged). `Host: api.stripe.com:80` goes to `https` on port 80; `api.stripe.com:9080` to `https` on 9080, or plain `http` there when the api sets `tlsOrigination: false`. With no port in `Host`, the call goes to 443. The setup guide warns that a client which writes `:80` out will reach the vendor on port 80.
5. **Plain only.** A redirected call is plain `http` by nature. Redirecting `https` would need TLS interception, which is not offered. A listener with the `tls` part takes only TLS, so redirected calls need a plain proxy port.
6. **Loops are stopped by the internal address guard.** If the name is made to point at the proxy for the proxy too (for example a rewrite rule for the whole cluster), the proxy's own lookup returns its own address, an internal IP, so the call gets `403` with `Proxy-Status: hotline; error=destination_ip_prohibited`. The setup guide says to point the name at the proxy only for app pods.

## Setup guide

### What the app changes


1. The vendor's base address: `https://api.stripe.com` becomes `http://api.stripe.com`. Keep the port as it is: the proxy uses it to reach the vendor, over `https`.
2. A default header with the integration ID: `Proxy-Authorization: Basic <base64 of "shop-a:x">`.

The proxy port must be plain (no `tls` part), since a redirected call is plain.

## Way 1: name lookup (recommended)

Make the vendor's name mean the proxy, only in the app's pod:

```yaml
spec:
  hostAliases:
    - ip: "10.0.0.5"            # the proxy Service's cluster address
      hostnames: ["api.stripe.com"]
```

The proxy Service must listen on the port the app uses (80 by default) and send it to the proxy port. List each vendor name. Don't point the name at the proxy for the whole cluster: the proxy's own lookup would then get itself back, and its guard refuses that call with `403`.

## Way 2: network rules in the app's pod

A start-up container with network rights rewrites the pod's outgoing plain calls on port 80 to the proxy, as Istio does. It catches every vendor without a list, but also the app's other plain calls inside the cluster, which then go through the proxy, and calls to internal addresses are refused by its guard. Exclude those addresses in the rule.

## Watch out

- A client that writes the port out (`Host: api.stripe.com:80`) reaches the vendor on port 80 over `https`, which usually fails. Leave the port out of the address.

## Tests

- End to end: "The transparent redirect" in `src/hotline-proxy/features/end-to-end.feature`. The Go client calls the test vendor's own name over plain `http`, its name lookup leads to the proxy, and it sends `Proxy-Authorization` as a default header; over plain HTTP/1.1 and plain HTTP/2.
- Port 80 is kept: a new row in "Choosing http or https to the vendor" in `host-header.feature`. The fake vendor's port 80 now answers both plain `http` and `https`.
- A name that leads back to the proxy: already covered by "A call to the proxy itself is stopped by the guard" in `host-header.feature`.

## How it is built

No proxy code changed. A redirected call is a Host header call, handled by `findIngress` and `vendorScheme` in `handler.go`.
