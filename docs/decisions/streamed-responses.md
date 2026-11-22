# Streamed response decisions (Peter, 2026-11-21)

From the streamed responses interview in the project thread (roadmap M2). Requirements and test cases: `streamed-replies/requirements.md` in the project files. What the proxy sends: [docs/otel-export.md](../otel-export.md), "Streamed responses" and "Stream items".

1. A streamed response is a response whose media type has an `itemSchema` in the spec (OpenAPI 3.2). Server-Sent Events (`text/event-stream`) come first; JSON Lines and JSON sequences follow in a later pull request. A streamed response whose spec gives no `itemSchema` is still skipped as `streaming`.
2. Each item gets a record of its own, `hotline.stream.item`, with its place in the stream, its size and its check results, as a WebSocket message does (Q1).
3. Each item gets a span of its own, `stream item`, under the `stream` span, with `item spec check` and `wait for check worker` under it when it was checked (Q2, follows from Q1).
4. Split like a WebSocket (Q3): the call is recorded once the response's headers reach the app, with no response body and its time ending there. The stream gets a record of its own, `hotline.stream`, and a span, `stream`, under `hotline call`, when it ends: its items, how many broke the spec, its bytes, their hash, how long it was open, and whether it ended `complete` or `cut`. Every record goes into the ledger, and the stream's hash is keyed with the stream's trace and span ids.
5. Item records wait for the call's record, and the stream record waits for every item's, so a reader always gets the call first and the stream last.
6. The events go on to the app as they come and never wait for a check. An event over 2 MiB is counted but not checked (`body_too_large`); a full spec check line skips an item's check (`overloaded`), never a byte.
7. Events are read by the HTML standard's rules, as OpenAPI 3.2 asks, and checked as the object 3.2 gives them: `data` as text, with `event`, `id` and `retry` when the event sets them. `retry` is kept only when it is all digits; an `id` holding a NUL byte is ignored.
8. A stream still open when the proxy stops is ended and recorded as `cut`, within the stop's time, as a WebSocket session is.
9. The first spec is Anthropic's messages stream, written by hand in `specs/anthropic/messages`, since Anthropic publishes no OpenAPI file (Q4). Revisit if Anthropic publishes one: then start from it.
10. The console shows no line per item and two lines for the stream: who and where, then `<< ended`, its items, how many broke the spec in red (green when none), its size and how long it was open (Q5).
11. The shop example gets a `support` app, integration `shop-support`, that answers a customer's question about an order with a streamed response from Anthropic's Go SDK. It runs only in the Floci workflow, with aimock playing Anthropic (Q6). Revisit if aimock can't stream Anthropic's events.
12. Known gap: with `content: full`, the file exporter saves no stream body yet. It comes with the `support` app.
13. The word is "streamed response", never "streamed reply", as [glossary.md](glossary.md) item 6 says for every answer of a vendor. In Go the stream is `responseStream`.
14. Item records wait for the call's record to be handed on, which happens once the headers reach the app. The forwarder sends a Server-Sent Events stream's headers before it reads its body, so no item comes before. Revisit when JSON Lines or JSON sequences come: a stream whose headers go with its first bytes must also free its items when the headers never reach the app.
