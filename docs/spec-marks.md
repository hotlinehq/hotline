# Hotline's marks in a vendor spec

Hotline reads our copy of each vendor's OpenAPI spec. Where plain OpenAPI can't say what the proxy needs, the spec carries a mark of our own, a field whose name starts with `x-hotline-`. OpenAPI lets any tool add fields starting with `x-`, and other tools ignore them.

This page lists every mark, what it is for, and an example. Marks live only in our copy of a spec or in an overlay (a small file that changes a spec when it is loaded), never in the config. A mark the proxy can't use stops it at start, and `hotline-proxy validate` reports it.

**Adding a mark.** Only when a real example needs it (Peter, 2026-11-01). Add it to this page in the same pull request.

| Mark | Where | What it's for |
|---|---|---|
| [`x-hotline-redact`](#x-hotline-redact) | a parameter, a response header, a multipart file property | which values may appear in the record |
| [`x-hotline-record`](#x-hotline-record) | a body field, a call header | copy one safe value into the record |
| [`x-hotline-body`](#x-hotline-body) | a body's media type | read a vendor's own content type as JSON or XML |
| [`x-hotline-action-header`](#x-hotline-action-header) | the top of the spec | the header that names the action, when every call has the same path |
| [`x-hotline-action-field`](#x-hotline-action-field) | the top of the spec | the body field that names the action, when every call has the same path |
| [`x-async-api`](#x-async-api-websocket-messages-in-asyncapis-shapes) | an operation that answers `101`, and `components` | the operations and messages of its WebSocket, written as in AsyncAPI 3 |
| [`x-hotline-match`](#x-hotline-match) | a field of a message's payload schema | which message a WebSocket message is |

## x-hotline-redact

**What it's for.** Records must never hold secrets or personal data. So by default the proxy hides every value in the path and the query (`REDACTED`), and keeps only a short list of harmless headers. This mark changes that for one value: `false` means "safe, keep it", `true` means "hide it".

**Use case.** A Stripe customer id in the path is safe and helps find a call, so it is kept. A Stripe search query may hold an email address, so it stays hidden.

```yaml
paths:
  /v1/customers/{customer}:
    get:
      parameters:
        - name: customer
          in: path
          x-hotline-redact: false   # url.full shows /v1/customers/cus_123
        - name: expand
          in: query                 # no mark: shows as REDACTED
```

**Rules.**

1. It goes on a path, query or header parameter, on a response header, or on a file property of a multipart body (rule 6). It takes `true` or `false` only.
2. Path and query values are hidden in records unless marked `false`. The file under `content: full` hides only those marked `true`, so a path value that is itself a secret, such as the parts of a Slack webhook address, is marked `true` (2026-11-23).
3. Headers kept without a mark: `content-type`, `content-length`, `date`, `retry-after`, `request-id`, `x-request-id` and any `ratelimit-*`. Any other header is left out, name and all, unless marked `false`. A header marked `true` shows as `REDACTED`.
4. Never recorded, whatever the mark: `authorization`, `proxy-authorization`, `cookie`, `set-cookie`, and the login headers and query values named in the spec's security section.
5. A parameter or header that points to a shared definition with `$ref` (for example `$ref: '#/components/parameters/Customer'`) takes the mark written there.
6. On a file property of a `multipart/form-data` body (one with `format: binary` or a `contentMediaType`), `false` shows the file name the app gave in the record's `hotline.request.files`; without it the name is `REDACTED` (Peter, 2026-11-24). The mark may sit on the property or on the schema it points to. The copy of the body kept under `content: full` follows the same mark: its file names read `REDACTED` unless marked `false`, and the rest is kept as sent, file bytes included (Peter, 2026-11-26; decision 54 in `docs/decisions/spec-checks.md`).

Detail: [otel-export.md](otel-export.md), [proxy-aws.md](proxy-aws.md).

## x-hotline-record

**What it's for.** The proxy never records bodies. But some body fields say something useful and safe, such as which queue a message went to. This mark copies that one field into the record, under the attribute name it gives.

**Use case.** SQS puts the queue address only in the body, so without the mark you can't tell in Grafana which queue a call went to.

```yaml
QueueUrl:
  type: string
  x-hotline-record: messaging.destination.name
```

The record then holds `messaging.destination.name = https://sqs.eu-west-1.amazonaws.com/192325781350/invoices`.

It also works on a header of the call, for example to record the action:

```yaml
parameters:
  - name: X-Amz-Target
    in: header
    schema: { type: string, const: AmazonSQS.SendMessage }
    x-hotline-record: messaging.operation.name
```

**Rules.**

1. On a body field of the call or the response (JSON, XML or form bodies), or of a WebSocket message, or on a header of the call. Never on a path, query or cookie value, a response header, or a login header.
2. Only plain values: text, a number, true or false. Never an object or a list, and never a field inside a list.
3. The name uses letters, digits, dots and underscores. Names starting like the proxy's own (`hotline.`, `http.`, `url.`, `server.`, `network.`, `error.`, `dns.`, `tls.`, `log.`) are refused. Prefer OpenTelemetry's standard names, such as `messaging.destination.name` or `db.collection.name`.
4. Text is cut at 256 characters. The value goes into full records only, signed with the rest, never into the counter.
5. A mark is a promise that the field holds no secret and no personal data.

Detail: [proxy-body-fields.md](proxy-body-fields.md).

## x-hotline-body

**What it's for.** The proxy checks a body only when it knows how to read it. It reads `application/json`, the XML types and form bodies on its own. Some vendors use their own name for JSON or XML, and this mark says which it is. It takes `json` or `xml`.

**Use case.** AWS sends SQS bodies as `application/x-amz-json-1.0`, which is JSON under another name.

```yaml
requestBody:
  content:
    application/x-amz-json-1.0:
      x-hotline-body: json
      schema: { $ref: '#/components/schemas/SendMessageRequest' }
```

**Rules.**

1. It goes on a media type of a call or response body. Any value other than `json` or `xml` is refused.
2. XML bodies are read by the schema's shape and OpenAPI's `xml` notes. A body with a DOCTYPE is refused, and namespaces are ignored.

Detail: [proxy-sqs-example.md](proxy-sqs-example.md), [proxy-xml-bodies.md](proxy-xml-bodies.md).

## x-hotline-action-header

**What it's for.** Some vendors send every call to the same path and name the action in a header. OpenAPI allows only one operation per path and method, so each action becomes its own route, written with the header value after `#`. This mark says which header that is.

**Use case.** Every SQS call is `POST /`, and the header `X-Amz-Target` says whether it sends, receives or deletes.

```yaml
x-hotline-action-header: X-Amz-Target
paths:
  "/#X-Amz-Target=AmazonSQS.SendMessage":
    post: ...
  "/#X-Amz-Target=AmazonSQS.ReceiveMessage":
    post: ...
```

A call to `/` with `X-Amz-Target: AmazonSQS.SendMessage` matches the first route, is checked against it, and its record's `url.template` is that route.

**Rules.**

1. It goes at the top of the spec. It belongs to the vendor's api, never to an integration.
2. A route matches only when the header has exactly its value. With the header missing or unknown, the call has no route and is not checked (`no_spec_route`).
3. A spec with `#Name=value` routes but no mark, or naming another header, is refused.

Detail: [proxy-aws.md](proxy-aws.md).

## x-hotline-action-field

**What it's for.** Like `x-hotline-action-header`, but the action is a field of the body, not a header.

**Use case.** SNS and STS send every call to `POST /` as a form body, and the action is the `Action` field.

```yaml
x-hotline-action-field: Action
paths:
  "/#Action=Publish":
    post: ...
```

A call whose body is `Action=Publish&TopicArn=...&Message=...` matches `/#Action=Publish`.

**Rules.**

1. It goes at the top of the spec. A spec names the action by a header or by a body field, never both.
2. Only a top-level field of a form or JSON body is read, once the whole body has arrived, and only from bodies up to 2 MiB, the size the proxy keeps for its checks.
3. With the field missing or unknown, the call has no route (`no_spec_route`). The call is still forwarded unchanged.
4. A spec with `#Name=value` routes but no mark, or naming another field, is refused.

Detail: [proxy-aws-query.md](proxy-aws-query.md).

## x-async-api: WebSocket messages in AsyncAPI's shapes

**What it's for.** After a `101`, a WebSocket carries messages both ways, which plain OpenAPI can't describe. AsyncAPI, the standard for message APIs, can. So the spec keeps one file per vendor, and the messages are written inside it in AsyncAPI 3's own shapes, under one key, `x-async-api` (Peter, 2026-11-21, Q1 and Q7). The `x-` keeps the file valid OpenAPI, so it still opens in other tools and passes linters. The proxy names, checks and records each message, and writes down which operation it belongs to and how it is tied to other messages.

**Use case.** Gemini Live sends `toolCall` and the app answers `toolResponse`, with the same id. A `toolResponse` without its `id` is a broken message that today nobody sees, and a store of the records can group a tool call with its answer by that id.

```yaml
paths:
  /ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent:
    get:
      responses:
        '101': { description: Switching to a WebSocket }
      x-async-api:
        operations:
          audio:
            action: receive        # the vendor receives it, so the app sends it
            messages: [{ $ref: '#/components/x-async-api/messages/realtimeInput' }]
          toolCall:
            action: send           # the vendor sends it
            messages: [{ $ref: '#/components/x-async-api/messages/toolCall' }]
            reply:
              messages: [{ $ref: '#/components/x-async-api/messages/toolResponse' }]
components:
  x-async-api:
    messages:
      realtimeInput: { payload: { $ref: '#/components/schemas/realtimeInput' } }
      toolCall:
        payload: { $ref: '#/components/schemas/toolCall' }
        correlationId: { location: '$message.payload#/toolCall/functionCalls/0/id' }
      toolResponse:
        payload: { $ref: '#/components/schemas/toolResponse' }
        correlationId: { location: '$message.payload#/toolResponse/functionResponses/0/id' }
```

**Rules.**

1. **Where.** `x-async-api` with its `operations` goes on the operation that answers `101`. The messages go in `components/x-async-api/messages`, so several routes can share them; each operation lists them by `$ref`. A route without the mark gets the session record's byte counts only.
2. **Sides.** Seen from the vendor, as OpenAPI is: `action: receive` means the app sends the operation's messages, `action: send` means the vendor sends them. A message in the operation's `reply` is sent by the other side.
3. **One operation per message.** A message belongs to exactly one operation of its route, in its `messages` or its `reply`. A message in two operations refuses the spec. A reply that can't say which request it answers, such as Kraken's failed subscribe, gets an operation of its own (Peter, 2026-11-21, Q8).
4. **A message** has a `payload`, a `$ref` to `components/schemas`, which is checked as a body is. It may have a `correlationId` and a `contentType`. Its name is its key in `components/x-async-api/messages`.
5. **Content type.** `contentType` on a message, or `defaultContentType` beside the operations, and `application/json` when neither is set. JSON messages are read from text and binary frames alike. A side whose messages are not JSON, such as `audio/pcm`, is not read: its messages are recorded as `unknown`, `hotline.schema.skipped=unsupported_content_type`. The messages of one side share one content type.
6. **`correlationId.location`** is `$message.payload#/` and then a JSON pointer to one field (Q3). A number in the pointer reads that item of a list: `/toolCall/functionCalls/0/id` is the first call's id, so a tool call with several calls records only the first. A text or number value is recorded as `messaging.message.conversation_id`; anything else records nothing. A WebSocket message has no headers, so `$message.header` refuses the spec. A `correlationId` may also be a `$ref`, such as to `components/x-async-api/correlationIds/reqId`.
7. **`reply`** takes `messages` only. `channel` and `address` refuse the spec, since one WebSocket is one channel and they would do nothing.
8. **Role.** Each message gets a role from where it is listed (Q9): `request` when its operation has a `reply`, `reply` when it is in a `reply`, `answer` when it has a `correlationId` but its operation has no reply and it is in no reply (it answers a request of any operation), and `push` otherwise. Take care: a business id, such as an order id, used as a push's `correlationId` makes that push an `answer`.
9. With `messages` in the api's `checks` (or `checks` left out), each named message is checked against its payload schema, up to 2 MiB after unpacking. Overlays change these the same way they change other parts of the spec. An unclear spec is refused: at start the proxy stops, at reload the last good spec stays.
10. The old marks are refused with a pointer here: `x-hotline-messages` (replaced by `x-async-api`) and `x-hotline-id-tuple` (replaced by the message's `correlationId`).

The proxy pairs nothing itself and keeps nothing per session (Q5): a wrong reply, a reply to nothing or an unanswered request is found later in the data store, from the role, the operation and the correlation id on each message's record and span.

## x-hotline-match

**What it's for.** It says which message a WebSocket message is: the first message of its side whose payload schema's marked fields all match names it. The messages of a side are tried by operation name, then in the order each operation lists them.

```yaml
toolResponse:
  properties:
    toolResponse: { x-hotline-match: true, type: object }   # must be there
sessionUpdate:
  properties:
    type: { x-hotline-match: true, const: session.update }  # must have this value
```

**Rules.**

1. It goes on a property of a message's payload schema and takes `true`. With a `const` beside it, the field must have that value; without, it must be there.
2. A message schema may mark several fields; a message is that message only when all of them match. No match: `unknown`, recorded but not checked.
3. The spec is refused when a message schema has no mark, when a mark sits inside a list, or when every mark of one message of a side is among another's, so the two would match the same messages.

## Other spec words the proxy reads

These are not our marks, but the proxy gives them a meaning worth knowing:

- **A query word after `#`** picks a route by its query, for vendors like S3: `/{Key+}#?tagging`. Several words join with `&`; a word with a value matches only that value. Detail: [proxy-xml-bodies.md](proxy-xml-bodies.md).
- **`{name+}`** takes the rest of the path, last part only, as in `/{Key+}`.
- **`contentMediaType` with `contentSchema`** on a text field: the text holds JSON, and it is checked against that schema. Used for the invoice job inside an SQS message. Detail: [proxy-sqs-example.md](proxy-sqs-example.md).
- **`contentEncoding: base64`** beside them: the text is base64, decoded before its check. Used for Kinesis records. Detail: [proxy-shop-example.md](proxy-shop-example.md).
- **`x-aws-pattern`**, written by `tools/aws-openapi`: an AWS pattern Go can't read. The proxy ignores it.
