# The shop example: four apps on AWS, through Hotline

*Agreed with Peter, 2026-11-01; steps 1 and 3 built the same day. Scenarios: `src/hotline-proxy/features/aws-shop.feature` (the shop's apis, and the whole story through the proxy) and `end-to-end-aws.feature`. The example is [examples/aws-shop](../examples/aws-shop/README.md). It grows the invoice example of [proxy-sqs-example.md](proxy-sqs-example.md) and [proxy-xml-bodies.md](proxy-xml-bodies.md).*

## What Peter asked for

More examples, for AWS only for now (Peter, 2026-11-01): DynamoDB, EventBridge, Kinesis and the other most used AWS apis, in one story a reader can follow.

## Decisions

1. **One program, four apps.** `examples/aws-sqs-invoices` is renamed `examples/aws-shop`, one Go program with a group of commands per app: `checkout`, `invoices`, `mailer`, `refunds` (Peter, 2026-11-01: "1 single program").
2. **Each app is its own integration.** Its name is the user name in the proxy address, so the records say which app made each call. Each integration lists only the apis that app uses.
3. **One script runs the whole story.** `run-shop.sh`: checkout pays, invoices makes the invoice, the mailer mails it, refunds starts a credit note, then two mistakes on purpose (a broken invoice job; an order written to a table that is not checkout's own), then a summary. It stops at the first failing step (Peter, 2026-11-01: "one sh script for one complete scenario").
4. **Existing AWS resources only.** The program and the script never create or remove anything. Only the queue `invoices` exists so far; Peter creates the rest with the names in the README, which lists the AWS commands to do it (Peter, 2026-11-01).
5. **One api per AWS resource**, each with the stable spec for its service from `specs/aws` and an overlay naming the resource and the shape of what is sent to it. The overlays live in `src/hotline-proxy/testdata/aws/`.
6. **Step 1** adds the JSON apis DynamoDB, EventBridge, Kinesis, Secrets Manager, SSM Parameter Store, KMS, CloudWatch Logs and Step Functions, beside SQS and S3.
7. **Step 3** adds SNS, STS, Lambda and SES (Peter, 2026-11-01: "add lambda", "add email"):
   - checkout publishes the invoice job, unchanged, on the SNS topic `order-paid`; the `invoices` queue listens with raw delivery, so it gets the job as published, and the worker doesn't change;
   - when it starts, the invoices worker takes on the role `invoice-worker` through STS (a role is a set of AWS rights; taking it on gives short-lived keys with only those rights). The role covers all the worker's AWS calls;
   - the worker asks the Lambda function `payment-qr` for the payment text a bank app reads from a QR code, and prints it on the invoice;
   - the mailer sends the email with SES v2 from `MAIL_FROM`, as a whole email with the PDF attached, or writes it to a folder with `--out`. The manual run sends to AWS's test inbox `success@simulator.amazonses.com`, which delivers nothing; the tests use a fake SES.
8. **Keys from STS name their account**, and newer SDKs would then send DynamoDB and Kinesis calls to a host named after the account, which the config doesn't list. The shop turns that off for every client (found by the whole-story test, 2026-11-01).

## What the proxy learned for it

- **Base64 text holding JSON.** Kinesis sends each record as base64 text. A string schema with `contentEncoding: base64` beside `contentMediaType` and `contentSchema` is decoded first, then checked. Text that is not base64 is reported with the rule `base64`.
- **AWS's patterns readable by Go.** AWS's models write patterns for Java's regular expressions. `tools/aws-openapi` rewrites the forms Go can't read (`\uXXXX`, `\p{Print}`), and keeps any pattern Go still can't read as `x-aws-pattern`, which the proxy doesn't check.
- **Body fields in the record**, each under `hotline.recorded.`. DynamoDB records `TableName` as `db.collection.name` and the action as `db.operation.name`; Kinesis and SNS record the stream or topic as `messaging.destination.name`; SNS records the action as `messaging.operation.name`; the others record the action as `rpc.method`.
- **Routes picked by a body field**, for SNS and STS, and the kept body grown to 2 MiB: [proxy-aws-query.md](proxy-aws-query.md).
- **The overlays of step 3.** `order-paid-topic` pins the topic and checks the message as the invoice job. `invoice-worker-role` pins the role. `payment-qr-function` pins the function, keeps its name in `url.full`, reads its input and output as JSON whatever content type the SDK sends, and keeps `X-Amz-Function-Error` in the record, so a failing function is reported (its output then lacks `qrText`). `invoice-email` checks that every address is an email address and copies none into the record. The email itself is base64 text and is not checked.

## Tests

- The program's own tests use small fakes that also play the forward proxy.
- `aws-shop.feature` checks each new api's overlay, and runs the whole story with the real program through the proxy inside the test, with a fake AWS for every host. Like the shop's EventBridge rule, the fake puts each `InvoiceReady` event on the mail queue; like the topic's subscription, it puts each published job on the invoices queue. It also checks that the STS keys and the email addresses never reach a record or a span, and that a broken job is reported by both checkout and the worker.
- `end-to-end-aws.feature` runs checkout through the real hotline-proxy program to a fake SNS over TLS. Only the order-paid topic can be reached this way: one integration can't name two apis with the same host, so the shop's other apis can't all be on `localhost`.

## Still to come

Bedrock, on plain REST. Not in step 3: SNS calls other than `Publish`, STS calls other than `AssumeRole`, SES templates and bulk sending, and Lambda's streamed responses.
