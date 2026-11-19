# A Go app sending to AWS SQS through Hotline, with a checked message

*Agreed with Peter and built, 2026-10-30. Scenarios: `src/hotline-proxy/features/aws-sqs.feature` (fast, with a fake SQS) and `end-to-end-aws.feature` (the example app and the real program). It builds on [proxy-aws.md](proxy-aws.md), [proxy-spec-checks.md](proxy-spec-checks.md) and [proxy-overlays.md](proxy-overlays.md). Peter's answers are at the end.*

## What Peter asked for

1. A complete, working example: a Go app that sends to AWS SQS through Hotline, with end-to-end tests.
2. A complete OpenAPI spec for SQS, so every SQS call the app makes is checked, not only `SendMessage`.
3. A check of the message itself. One integration sends a JSON message: a job asking for the invoice PDF of one order (Peter, 2026-10-30, in place of the first idea of a parquet aggregation job in S3).

## The use case

- **The shop** (the app that asks for the invoice) sends one message per paid order: "make the invoice for order `A-1001` with the template `invoice-standard-v2`, in English".
- **The worker** (the app that does the job) receives the message, looks up the order, renders the PDF with the template the message names, and deletes the message from the queue so it is not handled twice.
- **Hotline** sits between both apps and SQS. It records every call and checks it against the spec, including the job message inside it. It never blocks a call: a broken message still reaches SQS, and the check result shows up in the record.

## The job message (version 1)

A first proposal. Field names are open for Peter to change.

```json
{
  "schemaVersion": 1,
  "eventId": "5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10",
  "orderId": "A-1001",
  "templateId": "invoice-standard-v2",
  "language": "en",
  "requestedAt": "2026-10-30T19:30:00Z",
  "sendTo": "customer@example.com"
}
```

| Field | Required? | Rule | Why |
|---|---|---|---|
| `schemaVersion` | yes | whole number, exactly `1` | Lets the message change later without breaking old workers. |
| `eventId` | yes | `uuid` | The ID of this event (Peter, 2026-10-30). The worker uses it to notice an event it already handled (SQS may deliver a message twice). |
| `orderId` | yes | text like `A-1001`: a capital letter, a dash, digits | The order to make the invoice for. |
| `templateId` | yes | text of lower-case letters, digits and dashes, such as `invoice-standard-v2` | Which template renders the PDF (Peter, 2026-10-30). The worker refuses a template it doesn't know; the schema only checks the shape. |
| `language` | yes | one of `en`, `de`, `sk`, `cs` | The invoice's language. |
| `requestedAt` | yes | `date-time` | When the shop asked; helps when looking into a late invoice. |
| `sendTo` | no | `email` | Where to mail the PDF, if anywhere. Mailing it is not part of this step. |

Any other field is reported (`additionalProperties: false`), so a typo like `ordrId` is caught.

## How the message sits in the SQS spec

An SQS `SendMessage` body is JSON, but the message itself is a **string** inside it, in the field `MessageBody`:

```json
{"QueueUrl": "https://sqs.eu-west-1.amazonaws.com/192325781350/invoices",
 "MessageBody": "{\"schemaVersion\":1,\"eventId\":\"5f0c…\", …}"}
```

So the check has to read the string as JSON, then check that against the job schema. OpenAPI 3.1 already has words for this, from JSON Schema: `contentMediaType` (what the string holds) and `contentSchema` (the schema for what it holds).

```yaml
MessageBody:
  type: string
  contentMediaType: application/json
  contentSchema:
    $ref: '#/components/schemas/InvoiceJob'
```

Requirements:

1. **Strings holding JSON are checked** when the spec says `contentMediaType: application/json` with a `contentSchema`. The JSON Schema library the proxy already uses supports this; it only needs turning on. Without these words, a string stays a plain string, as today.
2. **Places carry on into the inner message.** A missing `orderId` inside the message is reported as `body $.MessageBody.orderId required`. A message that is not valid JSON is `body $.MessageBody json`. As always, results never hold a value from the call.
3. **The same in responses.** The worker's `ReceiveMessage` response holds the messages in `Messages[].Body`; our spec marks those the same way, so a bad message is also reported when the worker receives it (`response body $.Messages[0].Body.orderId required`).
4. **SQS's content type counts as JSON because the spec says so.** SQS sends `Content-Type: application/x-amz-json-1.0`. The proxy itself only reads `application/json`, `…+json` and form bodies, and knows nothing about AWS. The spec marks the media type with `x-hotline-body: json`, next to its schema, and the proxy then reads that body as JSON. The only allowed value is `json`; any other refuses the spec. `tools/aws-openapi` writes the mark on every request and response (Peter, 2026-10-30).

### Three integrations, three message schemas

There will be three integrations, each with its own queue and its own message schema, all starting from the same SQS spec (Peter, 2026-10-30). The queues differ in the path of their queue URL: `https://sqs.eu-west-1.amazonaws.com/192325781350/invoices`, `…/192325781350/<second queue>`, and so on.

One thing to know: with SQS's JSON protocol, every call goes to the path `/`. The queue URL is not in the call's path; it travels in the body, in the field `QueueUrl`. So the proxy cannot pick a route by queue, but the spec can check the field.

Requirements:

1. **One shared SQS spec, plus one overlay per queue** (Peter, 2026-10-30). Overlays are built (pull request #35, [proxy-overlays.md](proxy-overlays.md)). An overlay is a short file in the official OpenAPI Overlay 1.1 format; each of its actions says "at this spot in the spec, change this". Each queue's overlay adds two things: its own message schema on `MessageBody`, and its own queue URL on `QueueUrl` (an exact value, `const`). A message sent to the wrong queue is then reported as `body $.QueueUrl const`.
2. **Where the overlay changes the spec.** `MessageBody` and `QueueUrl` in `SendMessage`, in each entry of `SendMessageBatch`, and `Body` in each message of the `ReceiveMessage` response; `QueueUrl` also in every other action that names a queue (`ReceiveMessage`, `DeleteMessage`, and so on).
3. **The overlays are written by hand and checked by a test.** Each one is short. The `hotline-proxy overlay` command can also make one from the shared spec and a hand-edited copy. A test loads the example config with `validate`, so an overlay that no longer fits the shared spec (a warning) fails the test.

```yaml
apis:
  aws-sqs-invoices:
    hosts: [sqs.eu-west-1.amazonaws.com]
    spec: ../../specs/aws/sqs/2012-11-05.openapi.yaml   # the stable shared spec
    overlays: [./overlays/aws-sqs-invoices.overlay.yaml]
    vendorTimeout: 25s             # above SQS's 20 second long polling
  aws-sqs-<second>:
    hosts: [sqs.eu-west-1.amazonaws.com]
    spec: ../../specs/aws/sqs/2012-11-05.openapi.yaml   # the stable shared spec
    overlays: [./overlays/aws-sqs-<second>.overlay.yaml]
    vendorTimeout: 25s
  aws-sqs-<third>:
    hosts: [sqs.eu-west-1.amazonaws.com]
    spec: ../../specs/aws/sqs/2012-11-05.openapi.yaml   # the stable shared spec
    overlays: [./overlays/aws-sqs-<third>.overlay.yaml]
    vendorTimeout: 25s

integrations:
  - id: invoices
    apis: [aws-sqs-invoices]
  - id: <second>
    apis: [aws-sqs-<second>]
  - id: <third>
    apis: [aws-sqs-<third>]
```

The invoices overlay, in short:

```yaml
overlay: 1.1.0
info: { title: Invoices queue, version: "1" }
actions:
  - target: $.components.schemas
    update:
      InvoiceJob: { type: object, additionalProperties: false, required: [schemaVersion, eventId, orderId, templateId, language, requestedAt], properties: { … } }
  - target: $.components.schemas.SendMessageRequest.properties.QueueUrl
    update: { const: https://sqs.eu-west-1.amazonaws.com/192325781350/invoices }
  - target: $.components.schemas.SendMessageRequest.properties.MessageBody
    update: { contentMediaType: application/json, contentSchema: { $ref: '#/components/schemas/InvoiceJob' } }
  # … the same for SendMessageBatch entries, the ReceiveMessage response and QueueUrl elsewhere
```

## The complete SQS spec

- **It lives in the new `specs/` folder** at the top of the repo (Peter, 2026-10-30): `specs/aws/sqs/2012-11-05.openapi.yaml`, named after AWS's own version of the SQS api. That folder holds stable vendor specs only; overlays stay with their tests and examples. Its rules are in [specs/README.md](../specs/README.md).

- **All SQS actions**, one route each, in the agreed form `/#X-Amz-Target=AmazonSQS.SendMessage`, with `x-hotline-action-header: X-Amz-Target` at the top. Generated from AWS's Smithy model (AWS's own API description language), since AWS publishes no OpenAPI for SQS.
- **Every response** each action can give: `200` with its body, and SQS's error responses (`400` and `500` with `__type` and `message`).
- **Login** named in the security section as the `Authorization` header; as always, its value is never read.
- **Headers to record** marked as agreed: `x-amzn-RequestId` and `x-amzn-query-error` with `x-hotline-redact: false`.
- **Checked as a valid OpenAPI 3.1 file** by a test, and loaded by the proxy's own `validate` command.

This also builds what [proxy-aws.md](proxy-aws.md) agreed but did not build yet: routes named by a header, and the signed call test.

## The example app

**Built** (Peter asked for it, 2026-10-30): `examples/aws-sqs-invoices`, renamed [examples/aws-shop](../examples/aws-shop/README.md) on 2026-11-01, where these commands became the shop's `checkout` and `invoices` apps (see [proxy-shop-example.md](proxy-shop-example.md)). It is its own Go module, so the proxy's module never depends on the AWS SDK. Three commands:

- `request --order A-1001 --template invoice-standard-v2 --language en`: sends the invoice job to the queue.
- `send --body TEXT`: sends any text unchanged, to try Hotline's checks with a broken message.
- `work --out ./invoices [--once]`: receives jobs, checks each one itself, writes `invoice-A-1001.pdf` to the folder (creating it if needed), and deletes the message. Since 2026-10-31 it can store the PDF in S3 instead (`--bucket`), with `fetch`, `list` and `cleanup` for the shop: see [proxy-xml-bodies.md](proxy-xml-bodies.md). A message it can't use stays on the queue, for the queue's dead letter queue.

It sets up the SQS client exactly as the AWS setup guide says. Its own tests use a fake SQS that also plays the forward proxy, so they check what the app sends to Hotline. The end-to-end tests below, through the real proxy, come with the proxy changes.

## Tests

- `aws-sqs.feature` drives the proxy with a fake SQS: each action is its own route; a call with no or an unknown `X-Amz-Target` has no route; SQS's content type is read as JSON only where the spec marks it `x-hotline-body: json`, and another mark refuses the spec; AWS's error response is checked and its headers kept; a spec whose routes name a header it doesn't declare is refused; the recorded signed call reaches the vendor with every signed part unchanged, over the forward proxy and the Host header ingress, on HTTP/1.1 and plain HTTP/2; and, with the invoices overlay ([testdata/aws/invoices.overlay.yaml](../src/hotline-proxy/testdata/aws/invoices.overlay.yaml)), each mistake in the job is reported at its place inside `MessageBody`, in the `ReceiveMessage` response too, a message to another queue is reported, results never hold a value, and the plain spec doesn't check the message.
- `end-to-end-aws.feature` builds the example app and runs it against the real `hotline-proxy` program, started with the test's config ([testdata/aws/hotline-proxy.yaml](../src/hotline-proxy/testdata/aws/hotline-proxy.yaml), only its addresses changed), and a fake SQS over TLS: a job goes there and back with records for `SendMessage`, `ReceiveMessage` and `DeleteMessage` and no results; a broken job is still delivered, and reported.

## Found while building

- The JSON Schema library can check a string that holds JSON itself, but it reports the wrong place for that string (it points at whichever field was checked last). The proxy does this check itself instead, with the library's support for added keywords, so places are right.
- SQS leaves `Messages` out of an empty `ReceiveMessage` response. A fake SQS that sent `"Messages": null` was caught by the response check, as a real vendor's mistake would be.

## Open questions and answers

Peter said to go ahead with the proxy side (2026-10-30), taking the recommendations below, except where noted.

1. **A fake SQS or real AWS for the tests?** I recommend a small fake SQS inside the end-to-end tests: it answers `SendMessage`, `ReceiveMessage` and `DeleteMessage` like SQS (including the MD5 hash of the message, which the SDK checks). Tests then run anywhere, offline, with no AWS account or cost. Running against real AWS stays a manual step in the example's README. (LocalStack, a local copy of AWS run in Docker, is the other choice, but it adds Docker to the tests.)
2. **How do the three integrations stay in step with the SQS spec?** Settled: one SQS spec generated from the Smithy model, and one overlay per queue, applied by the proxy when it loads (built in pull request #35).
3. **What are the other two messages, and should the example show all three?** I recommend the example app and its end-to-end tests cover the invoice integration fully, and `checks.feature` uses a second integration on the same host to prove each one gets only its own schema and queue. The other two message schemas can be added once you name them.
4. **Where does the worker find the order and the template?** I recommend a small built-in list of made-up orders and two built-in templates in the example, so it needs no database or shop. The order's lines stay out of the message, which keeps the message small and the order data in one place.
5. **Where does the PDF go?** I recommend a local folder in this step. Storing it in S3 (and its calls going through Hotline with an S3 spec) is a later step. The PDF is a plain one-page file written with a small PDF library in the example's own module.
6. **Should the worker also refuse bad messages itself?** I recommend yes: Hotline only reports, it never blocks, so the worker checks the message against the same schema and moves a bad one aside (to a "dead letter" queue, the usual SQS place for messages that cannot be handled) instead of retrying it forever.

## Not in this step

- SQS's older query protocol (form bodies with `Action=SendMessage`).
- Other AWS services, including S3 through Hotline.
- Mailing the invoice to `sendTo`.
- Blocking calls whose message fails the check.
