# Body fields and headers in the record

*Agreed with Peter and built, 2026-10-31. Scenarios: `src/hotline-proxy/features/record-body-fields.feature`.*

The proxy never records bodies. But some body fields say something useful and safe, such as which SQS queue a message went to: SQS puts the queue address, `QueueUrl`, only in the body. A spec can mark such a field, and the proxy then copies that one field's value into the record.

## The mark

The mark is `x-hotline-record`, on a property of a body schema. Its value is a name, and the record holds the field under `hotline.recorded.` and that name, so a copied value never mixes with what the proxy measures itself (Peter, 2026-11-27).

```yaml
QueueUrl:
  $ref: '#/components/schemas/String'
  x-hotline-record: messaging.destination.name
```

The record then holds `hotline.recorded.messaging.destination.name`. `messaging.destination.name` is OpenTelemetry's standard name for a queue or topic, but the record never holds it under the plain name. A tool that needs the plain name can take the prefix off in its own pipeline; that is up to each user.

**Upgrading.** Before 2026-11-27 the record held the plain name, such as `messaging.destination.name` (`messaging_destination_name` in Grafana). A dashboard or alert on the plain name now finds nothing, with no error: change it to `hotline.recorded.messaging.destination.name`.

## Rules

1. **Where.** The mark goes on a property of a body schema, in the call or the response, for JSON bodies (and bodies the spec marks `x-hotline-body: json`) and form bodies, the two kinds the proxy reads for its checks. The proxy follows properties through objects and `$ref`.
2. **Only plain values.** The field must give its type as text (`string`), a number (`number`, `integer`), or true or false (`boolean`); `null` may be listed too. A mark on an object or a list stops the proxy at start.
3. **Never inside a list.** A field inside a list has many values, so it is not copied. Its mark is still checked.
4. **The name.** Letters, digits, dots and underscores only. A name that starts like the proxy's own (`hotline.`, `http.`, `url.`, `server.`, `client.`, `network.`, `error.`, `dns.`, `tls.`, `log.`, or is `messaging.message.conversation_id`) stops the proxy at start, so a copied field, even with `hotline.recorded.` taken off, never passes for one the proxy wrote. A name marked in both the call's and the response's body of one route stops the proxy too.
5. **Copied only when it fits.** The value is copied only when it is there and has the marked type. Otherwise nothing is recorded, and the checks report the problem as usual. A number too large for the record is left out.
6. **Text is cut** at 256 characters.
7. **A promise.** A mark is the spec author's promise that the field holds no secret and no personal data. Mark only fields like queue names, ids of things (not of people) and kinds.
8. **Full records only.** Calls with a known integration and api get these fields. They are covered by the record's hash with the rest of the record ([proxy-ledger.md](proxy-ledger.md)).
9. **Not in the counter.** The fields go into records only, never into `hotline.requests`, since a field could have very many values and make the counter costly (Peter, 2026-10-31). The counter itself was removed on 2026-11-21: exporters get no metrics.

## Headers

The same mark on a header parameter of the call copies that header (Peter, 2026-10-31):

```yaml
parameters:
  - name: X-Amz-Target
    in: header
    required: true
    schema: { type: string, const: AmazonSQS.SendMessage }
    x-hotline-record: messaging.operation.name
```

1. Only headers of the call. A mark on a path, query or cookie parameter, or on a response header, stops the proxy at start.
2. Never a login header: `Authorization`, `Proxy-Authorization`, `Cookie`, or a header the spec's security section names. Such a mark stops the proxy at start.
3. The value is copied as text, cut at 256 characters. A header sent more than once is joined with ", ". A missing header is not copied.
4. The name rules are the same as for body fields. A name may not be used on a header and in the call's body of one route.
5. Signed with the rest of the record, never in the counter.

## SQS

The shared SQS spec marks `QueueUrl` in every action's input with `x-hotline-record: messaging.destination.name`, so every queue's records name the full queue address under `hotline.recorded.messaging.destination.name`, such as `https://sqs.eu-west-1.amazonaws.com/192325781350/invoices` (Peter, 2026-10-31). `tools/aws-openapi` writes the mark when run with `-record QueueUrl=messaging.destination.name`; action outputs are never marked.

It also lists the `X-Amz-Target` header on every action, with the action's own value, marked `x-hotline-record: messaging.operation.name`, so records hold the exact header value under `hotline.recorded.messaging.operation.name`, such as `AmazonSQS.SendMessage` (Peter, 2026-10-31). The tool writes it when run with `-record-action messaging.operation.name`.
