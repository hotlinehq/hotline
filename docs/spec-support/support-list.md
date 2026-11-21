# What the edge proxy should support (draft)

Draft, 2026-11-21. Nothing built. The order of the work is in [roadmap.md](roadmap.md), and what the proxy can't read today in [gaps.md](gaps.md). Target: **OpenAPI 3.2** and **AsyncAPI 3**, the newest versions.

**Decided (Peter, 2026-11-21, Q1): OpenAPI 3.2 only.** The proxy refuses OpenAPI 3.1 and older, because spec files are easy to rewrite to 3.2. Revisit if a 3.1 rule turns out to clash with 3.2, so a file can't be moved over by changing its version line alone.

**Where the 3.2 items come from.** Checked on 2026-11-21 against the published 3.2.0 text (`versions/3.2.0.md` in `OAI/OpenAPI-Specification`) and the OpenAPI Initiative's upgrade guide, which says 3.2 has no breaking changes from 3.1.

## What dropping 3.1 means

- 3.2 adds to 3.1 and breaks almost nothing, so most 3.1 files load as 3.2 after changing one line: `openapi: 3.2.0`.
- Every spec in `specs/` and every test spec is 3.1 today. All are changed to 3.2 in one go.
- Vendors that publish 3.1 or older get that change in our copy of their spec. Overlays (small files that change a spec when it loads) are applied before the version is checked, so one could change it too. But the version belongs to the vendor's spec, not to one test or example, so our copy is the right place.
- The proxy refuses a file that says anything but 3.2, with a message saying to change it to 3.2.

## Must: the core of the edge proxy

Already works on 3.1 and carries over to 3.2:
1. Paths, methods, path and query and header and cookie parameters (`form`, `simple`, `deepObject` styles).
2. JSON, XML and form bodies, checked against JSON Schema Draft 2020-12.
3. Responses by status, including `2XX` and `default`, and response headers.
4. Security schemes, checked for presence only.
5. `$ref` within one file.
6. WebSocket messages in AsyncAPI 3 shapes under `x-async-api`.

New from 3.2:
7. **Version 3.2 loads** (`openapi: 3.2.*`).
8. **Streamed bodies with `itemSchema`.** 3.2's way to describe a body made of many items: Server-Sent Events (`text/event-stream`), JSON Lines and JSON sequences. Each event or line is checked against `itemSchema`. This closes the biggest gap: streamed replies from OpenAI and Anthropic.
9. **`QUERY` method,** and other methods listed under `additionalOperations`.
10. **`in: querystring`:** the whole query described as one value with its own schema.
11. **Cookie style** for cookie parameters.

## Should: next, because real vendors need them

12. **Multipart bodies** (file uploads): check the fields that are not files, using `encoding`, and 3.2's `itemEncoding` and `prefixEncoding` where given.
13. **Webhooks, minimal: the app calling the vendor.** Calls that set up webhooks (Stripe's webhook endpoints, GitHub's hooks) and calls to a vendor's webhook address (Slack or Discord incoming webhooks), checked like any call, with the secret in the address always hidden.
14. **Server variables:** a variable in the host is skipped, as the config names the hosts, and a variable in the path is read like a path value (built 2026-11-21).
15. **Server `name`** (new in 3.2): used to label which server a call went to.
16. **`discriminator`** with 3.2's `defaultMapping`: pick the branch by the field, so an error names the right branch.
17. **AsyncAPI traits:** refused since 2026-11-21, instead of ignored without a warning. Merge them in when a vendor needs it.

## Later: only when a real example needs it

18. `matrix`, `label`, `spaceDelimited` and `pipeDelimited` parameter styles.
19. Parameters described with `content` instead of `schema`.
20. 3.2's XML `nodeType`, the new way to say what `attribute` and `wrapped` say. Those two are deprecated in 3.2 but still allowed.
21. Non-JSON WebSocket messages, such as Protobuf or MessagePack.
22. Other AsyncAPI protocols, such as Kafka and MQTT, and standalone AsyncAPI files.

## Ignored on purpose: notes for humans, nothing to check

`links`, tags (and 3.2's tag `parent`, `kind` and `summary`), `summary` and `description` everywhere, examples (and 3.2's `dataValue` and `serializedValue`), `externalDocs`, `$self` (a document's own address), OAuth flows and scopes, 3.2's `oauth2MetadataUrl` and device flow, `deprecated`.

## Not supported

Calls from the vendor to the app: the spec's `webhooks` and `callbacks` sections describe them, so they are loaded and ignored (Peter, 2026-11-21).


OpenAPI 3.1, 3.0 and Swagger 2.0, and AsyncAPI 2.x. `$ref` to other files: specs are bundled into one file first.
