# Own OTLP encoder for records and spans (Peter, 2026-11-07 and 2026-11-08)

Scope, spike and numbers: [docs/otlp-encoder-scope.md](../otlp-encoder-scope.md). How it is sent: [docs/proxy-otlp-protocols.md](../proxy-otlp-protocols.md), points 10 to 15.

1. The call record and the call's spans are written as standard OTLP protobuf by the proxy itself, once for every destination. The OpenTelemetry log and trace exporters are gone; grpc-go stays for the gRPC connection. The config stays the same, but for the new `h2c` setting.
2. The counter `hotline.requests` and the proxy's own numbers stay on the OpenTelemetry metrics library.
3. The bar was at least 15% less proxy processor time on the small call at 5000 a second, and no worse on large calls. The spike met it.
4. gzip at its fastest level. `klauspost/compress` saved too little to add a module.
5. Traces stay; they are not made optional.
6. The old path is replaced outright, with no hidden switch.
7. `h2c: true` per exporter, for a plain `http://` endpoint only; HTTP/2 over TLS needs no setting.
8. Only the call span's parent, the app's span, is marked remote.
9. One pull request for the encoder, both senders and `h2c`.
