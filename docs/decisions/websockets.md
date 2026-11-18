# WebSocket decisions (Peter, 2026-11-18)

What happens today: [docs/proxy-websockets.md](../proxy-websockets.md). Spikes and drafts: `websockets/` and `shop-assistant/requirements.md` in the project files.

1. **One trace, separate spans.** When a WebSocket gets more than one record (the opening call, the session after it, and later its messages), they all share the trace ID and each has its own span ID (Peter, 2026-11-18). The proposal on the table: the opening call keeps its `hotline call` span, ending at the `101`; the session gets a child span from the `101` to the close; messages, if recorded, are log records under the session's span, not spans of their own.
2. **The WebSocket example is a support assistant in the shop**, on Google's Gemini Live api, answering questions about orders through a tool (Peter, 2026-11-18).
3. **A fake Gemini plays the vendor** in the tests and the example, since the real Gemini costs money and needs a key (Peter, 2026-11-18).
