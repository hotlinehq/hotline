# SQS example decisions (Peter, 2026-10-30)

Full wording in [docs/proxy-sqs-example.md](../proxy-sqs-example.md).

1. The example: a shop asks for an invoice PDF by putting a job on SQS; a worker renders it. Job fields: `schemaVersion`, `eventId` (not job ID), `orderId`, `templateId`, `language`, `requestedAt`, optional `sendTo`.
2. Three integrations will each have their own queue and message schema on one shared SQS spec, through one overlay per queue that pins `QueueUrl` and adds the message schema. Overlays live with tests and examples, never in `specs/`. The invoices overlay and its proxy config are test data, together in `src/hotline-proxy/testdata/aws/` (Peter, 2026-10-30); the end-to-end test runs that config with only its addresses changed.
3. Stable vendor specs live in `specs/<vendor>/<api>/<version>.openapi.yaml`; the SQS one is generated from AWS's Smithy model, and a test keeps it in step.
4. Tests use a fake SQS, never real AWS; running against AWS is a manual step in the example's README.
5. Other AWS services (S3, DynamoDB) come later.
