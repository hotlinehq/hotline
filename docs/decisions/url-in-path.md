# Vendor URL in path decisions (Peter, 2026-10-29)

Full wording in [docs/proxy-url-in-path.md](../proxy-url-in-path.md).

1. Two slices: vendor URL in path first, over HTTP/1.1; then the Host header ingress with HTTP/2 from apps.
2. Only the clean `/http://` or `/https://` form. A squashed slash or an encoded address gets `400`.
3. The vendor gets its own host (and port) from the path in `Host`, never the proxy's.
4. Every request the proxy reads and refuses (`400`, `501`) still gets a minimal record and is counted, on every ingress. Plain HTTP on a TLS port stays without a record.
5. Every record of a call that reached a vendor holds the vendor IP the proxy connected to (`network.peer.address`), minimal records too. Every record also holds the app's IP, from its connection, as `client.address` (Peter, 2026-10-31).
6. No loop guard in this slice; the loop question moves to the Host header slice.
