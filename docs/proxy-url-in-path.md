# The vendor URL in path ingress: scope and test cases

*Agreed with Peter, 2026-10-29, and built. The source of truth is `proxy-spec.md` in the `hotline-strategic` repo.*

## What the feature is

An ingress is a way an app reaches the proxy. Slice 1 built the forward proxy ingress, where the app puts the full vendor address in the request line. This slice adds the **vendor URL in path** ingress: the app puts the whole vendor address after the proxy's own address.

```http
POST /https://api.stripe.com/v1/payment_intents?expand=customer HTTP/1.1
Host: localhost:8080
Proxy-Authorization: Basic dGVzdC1zaG9wOng=
Content-Type: application/x-www-form-urlencoded

amount=1000&currency=eur
```

The app only changes the base address it calls, to `http://localhost:8080/https://api.stripe.com`, which almost every vendor library allows. It is the one ingress that worked in every library we tested (`egress-proxy-options.md`, client table), so it is the one to recommend first.

The ingress finds two things, the integration and the vendor's real address. After that the call takes exactly the same steps as a forward proxy call: TLS origination, timings, checks, record and export.

## Requirements

1. **Two slices** (Peter, 2026-10-29). This slice builds the vendor URL in path ingress over HTTP/1.1, plain or over TLS to the proxy. The Host header ingress and HTTP/2 from apps come in the next slice; until then, Host header calls keep getting `501`.
2. **Which calls belong to it.** A path that starts with `/http:` or `/https:`, plain or encoded (`/https%3A`), the scheme in any case. Only the clean form `/http://` or `/https://` is used; the damaged ones are refused (requirement 3). The rest of the path, with the query, is the vendor's address: `/https://api.x.com/v1/a?b=1` calls `https://api.x.com/v1/a?b=1`, and `/https://api.x.com` calls the path `/`. The proxy reads the raw request line, since many routers merge `//` into `/`.
3. **Damaged addresses are refused** (Peter, 2026-10-29). A squashed slash (`/https:/api.x.com/…`) or an encoded address (`/https%3A%2F%2Fapi.x.com/…`) gets `400` with `Proxy-Status: hotline; error=http_request_error`, and nothing is sent to any vendor. So does an address with no host (`/https:///v1`). The rule can be loosened later if a real client needs it.
4. **The vendor sees its own name** (Peter, 2026-10-29). The proxy sets `Host` to the host, and the port if one was given, from the path. The app's `Host` (the proxy's own address) never reaches the vendor.
5. **The same rules as the forward proxy** (earlier decisions): the scheme in the path is used, with TLS origination turning `http` into `https` and keeping the app's port; `not-passed` calls go out exactly as sent (since 2026-11-01, `http` turns into `https` for them too, unless the `not-passed` entry sets `tlsOrigination: false`); `Proxy-Authorization` carries the integration ID and is always removed; the proxy adds no forwarding headers.
6. **The record** is the same as for a forward proxy call, with `hotline.ingress` set to `url_in_path`. `url.full` and `server.address` are the vendor's, never the proxy's.
7. **Refused calls are recorded** (Peter, 2026-10-29). When the proxy refuses a request it has read, with `400` or `501`, it still sends a minimal record and counts the call: `hotline.integration.id`, `hotline.ingress`, `http.request.method`, `http.response.status_code`, `error.type` (`http_request_error`) and the timings, plus `server.address` when the proxy could read a vendor host. This applies to every ingress, so it also changes the forward proxy's refusals built in slice 1, and the `501` for Host header and CONNECT calls (a CONNECT record has no `hotline.ingress`, since CONNECT is not an ingress). One exception stays: plain HTTP sent to a TLS port is a failed TLS setup, not a request, so it keeps having no record and is counted in the proxy's own health data.
8. **The vendor IP in every record** (Peter, 2026-10-29). Minimal records now also hold `network.peer.address`: the IP the proxy connected to, after DNS and after any try at a second address. Full records already had it. Refused calls reach no vendor, so they have none.
9. **No loop guard in this slice.** A call to the proxy itself (`/http://localhost:8080/https://api.x.com/…`) passes through the proxy twice, each pass removing one address, so it ends on its own. The loop question moves to the Host header slice, where a call to the proxy itself would never end.

## Tests

- Feature scenarios: `src/hotline-proxy/features/url-in-path.feature`, and "Refused calls are still recorded" in `ingress.feature`.
- End to end: the Go client calls with a base address in the path, with an `https` and an `http` vendor address (`end-to-end.feature`), and over TLS to the proxy (`end-to-end-tls.feature`).

## How it is built

- `findIngress` in `handler.go` reads the raw request line (`RequestURI`), never the cleaned path. A target starting `/http:` or `/https:`, with the colon plain or encoded as `%3A`, in any case, is this ingress; only `/http://` and `/https://` followed by a host are parsed, the rest get `400`.
- From there the call takes the forward proxy's path unchanged: the vendor address from the path replaces the request URL for the vendor, and `Host` is set from it.
- A refused request (`400` or `501`) gets its minimal record from `refused` in `record.go`: ID, ingress (none for CONNECT), the vendor host when one could be read, method, status, `error.type=http_request_error` and `hotline.timing.total_us`. It goes through the same sink as every other record.

## Not in this slice

The Host header ingress; HTTP/2 from apps; accepting damaged addresses; a loop guard; the transparent redirect ingress; CONNECT.
