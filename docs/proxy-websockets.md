# WebSockets through the proxy

*What happens today, found by a spike on 2026-11-18 that drove real WebSockets through the proxy. The spike and its prototypes are in `websockets/` in the project files. Decisions so far: [decisions/websockets.md](decisions/websockets.md).*

A WebSocket starts as an ordinary HTTP call: the app sends `GET` with `Upgrade: websocket`, and the vendor answers `101 Switching Protocols`. After that the connection stays open and carries messages both ways, in small pieces called frames, for as long as both sides like.

## What works

The forwarder passes the upgrade on, then copies the bytes both ways, unchanged, until either side ends ([forwarder.md](decisions/forwarder.md)). The vendor timeout covers only the wait for the `101`, so a long WebSocket is not cut.

| Ingress | A WebSocket through it |
| --- | --- |
| Forward proxy (`ws://` address through the proxy) | works |
| Vendor URL in path | works |
| Host header | works |
| CONNECT, then TLS (`wss://` through the proxy) | works when the app offers `http/1.1`, or nothing, during TLS setup |
| CONNECT to port 80, plain `ws://` inside | works |
| Transparent, TLS | works |
| Transparent, plain `ws://` | refused as `not_tls`, like any plain HTTP there |
| Capture, TLS or plain `ws://` | works |

An app that offers `h2` inside a tunnel gets HTTP/2, on which a classic WebSocket can't start. Server-side WebSocket libraries offer `http/1.1` or nothing. WebSockets over HTTP/2 (RFC 8441) are not offered.

## What one WebSocket records today

One record, made when the WebSocket ends, with status `101`. The vendor's frames are counted as the reply body (`http.response.body.size`, `hotline.response.body.hash`, and the body under `content: full`). The app's frames are not counted. The call's time is the whole time the WebSocket was open.

## Known problems

Found by the spike, not fixed yet:

1. Every WebSocket ends with a recovered crash in the log: the handler flushes a connection the forwarder already took over, and Go's HTTP server logs a warning with a stack trace.
2. A WebSocket to an api with a spec is marked invalid: the reply check reads the vendor's frames as the body of the `101`, and fails with `not_in_spec`.
3. Only the vendor's side is in the record and the ledger.
4. Up to 2 MiB of the vendor's frames are kept for the reply check for as long as the WebSocket is open.
5. The call's time is the WebSocket's whole life, which skews latency numbers built on the records.
6. A WebSocket still open when the proxy stops holds the stop for the whole shutdown time and ends with no record (read from the code, not tried).
7. Under `content: full`, a vendor key sent in a header (such as Gemini's `x-goog-api-key`) is written into the file as sent. This holds for plain HTTP calls too.

## Planned

Requirements are being drafted; nothing below is built.

- The opening call and the session after it become two records in one trace, each with its own span ([decisions/websockets.md](decisions/websockets.md)).
- A shop assistant app on Gemini Live, with a fake Gemini for the tests, as the WebSocket example.
- Later: reading messages on both sides, checking them against an AsyncAPI document, and pairing replies with their requests.
