# Kraken Spot WebSocket v2

`v2.openapi.yaml` describes Kraken's Spot WebSocket API v2, both of its WebSockets: `ws.kraken.com/v2` for market data, with no login, and `ws-auth.kraken.com/v2` for trading, with a token inside each request. Each is one `GET /v2` that Kraken answers with `101 Switching Protocols`. After the `101`, the messages both ways are listed in AsyncAPI 3's shapes under `x-async-api` (see `docs/spec-marks.md`): 40 operations and 65 messages, named by their constant fields (`x-hotline-match`), with `req_id` as the correlation id that ties each reply to its request.

Kraken publishes no OpenAPI or AsyncAPI file for v2. This one is written by a script from an unofficial AsyncAPI file that we wrote from Kraken's 19 v2 reference pages (docs.kraken.com, read 2026-11-20), and every JSON example on those pages matches exactly one of its messages. Change the AsyncAPI file or the script, then write this file again; don't edit it by hand. Both live in the Hotline project files (`kraken-v2/kraken-v2.asyncapi.yaml` and `kraken-e2e/to_openapi.py.txt`).

It is used by the proxy's WebSocket message scenarios (`src/hotline-proxy/features/websocket-messages.feature`) and by the Kraken load case (`load/`). The real Kraken is never called: both run against a fake.
