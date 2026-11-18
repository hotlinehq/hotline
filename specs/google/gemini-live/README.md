# Gemini Live, opening call

`v1beta.openapi.yaml` describes the one HTTP call that opens a [Gemini Live](https://ai.google.dev/api/live) session: a `GET` on the `BidiGenerateContent` address (or `BidiGenerateContentConstrained`, for an ephemeral token), which Google answers with `101 Switching Protocols`. After the `101` the connection carries the session's WebSocket messages, which this file does not describe: they belong in an AsyncAPI document beside it, in WebSocket milestone 2.

Written by hand on 2026-11-18 from Google's Live API reference and the addresses that Google's Go SDK, `google.golang.org/genai` v1.79.0, opens. Google publishes no OpenAPI file for it. `v1beta` is the version in Google's address.

The login is in the security section, so Hotline hides it in every record and, under `content: full`, in the file too:

- `x-goog-api-key`, the api key the SDKs send as a header;
- `access_token`, the ephemeral token in the query of the constrained address.
