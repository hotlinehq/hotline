# WebSocket decisions (Peter, 2026-11-18)

From the WebSocket interview in the project thread. Requirements: `shop-assistant/requirements.md` in the project files. What the proxy sends: [docs/otel-export.md](../otel-export.md), "WebSocket sessions".

1. Two milestones (Q1): first see and prove what both sides said, then check messages against an AsyncAPI document, count them and put the results on the session record.
2. Milestone 1 counts only bytes (Q2): per direction, the WebSocket's frames after the `101` as they passed, compressed or not, without TLS; and how long the session was open. No frames are read.
3. Counting (Q3): the opening call counts once in `hotline.requests`, with status 101. The session is not counted, as a `CONNECT` tunnel that opens fine is not. Message counts come with milestone 2, in a counter of their own.
4. The opening call and the session are two records in one trace (Q5): the call is recorded when the `101` reaches the app, with no reply body and its time ending there; the session gets its own record, `hotline.websocket.session`, and its own span, `websocket session`, a child of `hotline call` from the `101` to the end.
5. Each direction is hashed for the ledger, keyed with the session's trace and span ids, under `hotline.websocket.from_app.hash` and `hotline.websocket.from_vendor.hash`.
6. A session still open when the proxy stops is ended and recorded within the stop's time.
7. Who ended the session and its close code are left to milestone 2, read from the close message itself (Q10). The proxy can see only which connection closed first, which in a WebSocket is nearly always the vendor's.
8. The sink tells a session record from a call record by `export.Call.Event`, empty for a call (Q14). Only a call counts in `hotline.requests`, and the console shows a session in two lines of its own. Revisit if a session record needs things a call never has inside the sink, such as its own queue; then a separate type.
9. The forwarder tells the handler when the `101` has reached the app, with the optional `forward.Switcher` (Q15; [forwarder.md](forwarder.md), item 8).
10. Revisit if a WebSocket must be checked over HTTP/2, or plain `ws://` on the transparent listeners: both are left for later.

## The shop assistant (Peter, 2026-11-18)

The example that shows milestone 1 at work, in `examples/aws-shop`. Requirements: `shop-assistant/requirements.md` in the project files.

11. A fourth app, `assistant`, integration `shop-assistant`: it talks to Gemini Live through Google's Go SDK, text only, with one tool, `orderStatus(orderId)`, which reads the order from the DynamoDB `orders` table. It waits for the turn that holds the answer after a tool call. Settings: `GEMINI_API_KEY`, `GEMINI_MODEL`, `GEMINI_BASE_URL`.
12. The opening call is checked against [specs/google/gemini-live](../../specs/google/gemini-live/), whose security section names `x-goog-api-key`, so the key is in no record, span or log, even with content: full. The Floci run fails if it finds the key in its records or log.
13. Gemini Live is played by aimock (a scripted fake of AI vendors, `ghcr.io/copilotkit/aimock`, pinned by digest) behind a small TLS front (socat), reached over `wss://` through `CONNECT`, which Hotline opens with a test certificate authority made for the run.
14. The assistant runs only in the Floci workflow, on Linux (Q16). The example needs no run against real AWS or real Gemini; aimock and its front start in `floci/up.sh`. Revisit if the story must run against real AWS or real Gemini, or on a Mac.
