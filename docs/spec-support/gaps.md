# What Hotline does not support from OpenAPI and AsyncAPI

What the proxy can't read today, checked 2026-11-21 against `hotlinehq/hotline` main (commit f00ecbf), mostly `src/hotline-proxy/internal/spec/` and `internal/santhosh/`.

What the edge proxy will support is in [support-list.md](support-list.md), and the order of the work in [roadmap.md](roadmap.md).

How to read the marks:
- **[code]**: read in the code on main.
- **[inferred]**: follows from the code, but not run or tested.
- **Matters**: how much the gap hurts recording and checking vendor traffic. **High** means real vendors hit it and a bad call can pass, or a good call is flagged. **Low** means rare, or only a note in the spec.

One fact first: the schema checker on main is the santhosh JSON Schema library (Peter's fork), set to Draft 2020-12. Katydid lives only on the `katydid` branch. So the JSON Schema section below is short; the katydid gaps apply only if that branch is merged.

## 1. Spec versions

Only the newest versions need support (Peter, 2026-11-21), so OpenAPI 3.0, Swagger 2.0 and AsyncAPI 2.x are not gaps.

| Gap | Matters | Source |
|---|---|---|
| OpenAPI 3.2, the newest version, is refused: only `openapi: 3.1.*` loads. So its new parts (`QUERY` method, `additionalOperations`, `itemSchema` for streamed bodies, `in: querystring`) are out too. | Medium: `itemSchema` is how 3.2 describes Server-Sent Events | [code] `spec.go` Parse |
| A standalone AsyncAPI file is not read. Messages exist only inside an OpenAPI file, under `x-async-api`. | Medium | [code] `messages.go` |

## 2. OpenAPI: servers and paths

| Gap | Matters | Source |
|---|---|---|
| Only the first `servers` entry is read, and only for its base path (like `/v1`). Other servers, and `servers` on a path or an operation, are ignored. | Medium | [code] `spec.go` |
| Server variables: a variable in the host (`https://{region}.api.com/v1`) makes Go refuse the address, so the base path `/v1` is silently dropped and routes miss. A variable in the path is treated like a path value. | Medium | [inferred], checked with Go's URL parser |
| `$ref` to another file (a split spec) is refused. Specs must be bundled into one file. | Medium | [code] `compile.go` object |
| `webhooks` (the 3.1 top-level section) is not read. Calls the vendor makes to the app are not checked. | Medium: Stripe, GitHub, Slack send webhooks | [code] only `paths` is read |
| `callbacks` are not read. | Low to medium | [code] |
| `links` are not read. | Low: a note for humans | [code] |

## 3. OpenAPI: parameters

| Gap | Matters | Source |
|---|---|---|
| Styles: only `form`, `simple` and `deepObject` are read. `matrix` and `label` path values (`;id=5`, `.5`) and `spaceDelimited` and `pipeDelimited` query lists are read as plain text or split on commas, so good calls can be flagged. | Low: rare in vendor APIs | [inferred] from `read.Shape` |
| An object in a query or header that is not `deepObject` (for example `?lat=1&lng=2` as one exploded object, or `a,1,b,2`) is not built into an object before its check. | Low to medium | [inferred] from `read.Shape` |
| A parameter described with `content` instead of `schema` (JSON inside a query value) is not checked at all. | Low | [code] `schemaOf` reads only `schema` |
| `allowReserved` and `allowEmptyValue` are ignored. | Low | [code] |
| Unknown request headers and unknown cookies are not reported; only unknown query names are. | Low: by design, apps send many headers | [code] `unknownQuery` |
| `Accept`, `Content-Type` and `Authorization` header parameters are skipped, as OpenAPI says. | None | [code] |

## 4. OpenAPI: request and response bodies

| Gap | Matters | Source |
|---|---|---|
| Only JSON (and `+json`), XML and `application/x-www-form-urlencoded` bodies are read. `multipart/form-data` (file uploads: OpenAI files, Twilio media), `text/plain`, `application/octet-stream` and NDJSON are skipped as `unsupported_content_type`. | Medium to high: uploads are common | [code] `check.go` reads |
| `encoding` (per-field content types in forms and multipart) is ignored. | Low to medium | [inferred] never read |
| Streamed responses: `text/event-stream` (Server-Sent Events, used by OpenAI and Anthropic streaming) is skipped as `streaming`. Its events are not checked. | High for AI vendors | [code] `CheckResponse` |
| Bodies over 2 MiB are skipped (`body_too_large`). | Medium | [code], a chosen limit |
| Unknown response headers are not reported; a status the spec doesn't list is. | Low | [code] |

## 5. OpenAPI: security schemes

| Gap | Matters | Source |
|---|---|---|
| Only whether a credential is present is checked, never its form. `http` basic and bearer, `oauth2` and `openIdConnect` all mean "some Authorization header". | Low: by design, credentials are never read | [code] `readSchemes` |
| OAuth scopes are ignored. | Low | [code] |
| `mutualTLS` sends nothing the proxy sees, so it counts as no login. | Low | [code] |

## 6. JSON Schema (santhosh, Draft 2020-12)

| Gap | Matters | Source |
|---|---|---|
| `discriminator` is not used. `oneOf` and `anyOf` still check, but the mapping is not enforced and an error names no branch. | Low to medium | [inferred]: not a JSON Schema keyword |
| Formats checked: only date-time, date, email, uuid, uri, ipv4, ipv6, int32, int64. Others (hostname, time, duration, uri-reference, regex and more) pass anything. Unknown formats pass too. | Low | [code] `ignoredFormats` |
| `contentMediaType` with `contentSchema` is checked only for `application/json`, with optional base64. Other inner types are not. | Low | [code] `jsonText` |
| Remote `$ref` and `$id` addresses outside the spec are refused. | Low | [code] `outside` loader |
| `xml` notes: namespaces are ignored and a DOCTYPE is refused. | Low | docs/spec-marks.md |
| `example`, `examples`, `default`, `readOnly` and `writeOnly` are notes only; a `readOnly` field sent in a request is not flagged. | Low | [inferred] library default |

If katydid replaces santhosh: `contains`, `prefixItems`, `propertyNames` and `unevaluated*` are silently ignored, and `uniqueItems`, `$defs` references and boolean schemas are refused. Detail: `katydid-sole-engine/draft2020-12-results.md` in the project files. That would turn several Low rows above into High.

## 7. AsyncAPI (inside `x-async-api`)

| Gap | Matters | Source |
|---|---|---|
| Protocols: only WebSocket after an HTTP `101`. Kafka, MQTT, AMQP, NATS, SNS/SQS as AsyncAPI, and SSE described as AsyncAPI are not read. | Medium: SQS and SNS are covered by OpenAPI instead | [code] |
| `servers` and `channels` are not read. One WebSocket is one channel; the route's OpenAPI path stands in for the channel address. | Low for WebSockets | [code] only operations and defaultContentType are allowed |
| Channel parameters (values in the address) are not read; the OpenAPI path and query parameters cover the WebSocket's URL. | Low | [code] |
| Bindings (`ws` query and headers, and others) are ignored silently. | Low: OpenAPI parameters cover the upgrade call | [code] key never read |
| Traits (operation and message) are ignored silently, so anything a trait adds, such as a `correlationId` or `contentType`, is lost. | Medium: vendors use traits to share these | [code] key never read |
| Message `headers` are not read, and `correlationId` in `$message.header` refuses the spec. WebSocket messages have no headers. | Low for WebSockets | [code] `correlation` |
| `correlationId` reads one field only; a list index reads one item, so a tool call with several calls records only the first id. | Medium for multi-call AI tool use | [code] and docs/spec-marks.md |
| `reply.address` and `reply.channel` refuse the spec (dynamic reply addresses such as a reply topic). | Low for WebSockets | [code] |
| `schemaFormat` (Avro, Protobuf, RAML schemas) is not supported: a payload must be a `$ref` to an OpenAPI JSON Schema. A non-JSON `schemaFormat` key is ignored silently. | Low to medium | [code] payload rule; [inferred] key ignored |
| An inline payload schema is refused; it must be a `$ref` to `components/schemas`. | Low: a style rule | [code] |
| Non-JSON messages (binary, `audio/pcm`, Protobuf, MessagePack) are recorded as `unknown` and not checked. One content type per side. | Medium: realtime audio APIs | [code] |
| Messages are named only by `x-hotline-match` marks; there is no AsyncAPI way to name them, and a match mark inside a list is refused. | Low: our own choice | [code] |
| Operation `security`, `tags`, `externalDocs`, message `examples` and `summary` are ignored. | None | [code] |
| No pairing in the proxy: an unanswered request or a wrong reply is found later, in the data store. | By design (Q5) | docs/spec-marks.md |

## What looks worth doing first

1. Server-Sent Events: check each streamed event (the OpenAI and Anthropic case).
2. Multipart bodies: at least check the non-file fields.
3. Server variables in the host: refuse the spec instead of silently dropping the base path.
4. AsyncAPI traits: refuse them, or merge them in, instead of ignoring them silently.
