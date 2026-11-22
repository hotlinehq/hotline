# Anthropic Messages, create a message

`2023-06-01.openapi.yaml` describes the one call that asks Claude for a message: `POST /v1/messages`. Anthropic answers with the whole message as JSON, or, when the request sets `stream: true`, as Server-Sent Events (a response sent as a series of small events, each with a name and a line of JSON). The spec gives the events as OpenAPI 3.2's `itemSchema`, so Hotline checks each event as it passes. `2023-06-01` is the first `anthropic-version`. The current SDKs, such as the Go SDK v2.13.0, send `2026-10-21`, whose changes (no sampling settings, documents and citations, the Admin api) leave this call's stream as it is, so the one spec covers both.

Written by hand on 2026-11-22 from two sources, since Anthropic publishes no OpenAPI file (its SDKs are made from a private one):

- Anthropic's streaming page, `https://platform.claude.com/docs/en/build-with-claude/streaming.md`: the events, their order and full examples.
- Anthropic's Go SDK, `github.com/anthropics/anthropic-sdk-go` v2.13.0 (commit `698c61f`, 2026-11-20), file `message.go`: the fields of each event, the content block types, the delta types and the stop reasons.

Only what those two show is checked. Fields the docs leave out of their examples are not required, and fields Anthropic adds later pass, since no object here refuses extra fields. A new event name or a new block or delta type is reported, so a change in Anthropic's stream shows up in the records.

The login is in the security section, so Hotline hides it in every record and, under `content: full`, in the file too:

- `x-api-key`, the api key as older SDKs send it, as a header of its own.
- `Authorization: Bearer`, the api key as the current SDKs send it (the Go SDK v2.13.0 does, found with the shop's `support` app on 2026-11-22). Hotline hides `Authorization` always, and the spec says it too.

`anthropic-version` is marked `x-hotline-redact: false`, so the record shows which version the app asked for.
