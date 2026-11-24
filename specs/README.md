# Vendor specs

Stable OpenAPI specs of the vendor apis Hotline knows, ready to point an api at in the proxy config (Peter, 2026-10-30).

## Layout

One folder per vendor and api. Each file is named after the vendor's own version of that api:

```
specs/
  aws/
    sqs/
      2012-11-05.openapi.yaml   # SQS, JSON protocol; 2012-11-05 is AWS's version name for the SQS api
      README.md                 # where it came from and how it was made
    s3/
      2006-03-01.openapi.yaml   # S3, REST and XML protocol; the bucket is in the host name
      README.md
    dynamodb/2012-08-10.openapi.yaml          # and one folder each, with its README, for the JSON apis
    eventbridge/2015-10-07.openapi.yaml       # the shop example uses (2026-11-01)
    kinesis/2013-12-02.openapi.yaml
    secrets-manager/2017-10-17.openapi.yaml
    ssm/2014-11-06.openapi.yaml
    kms/2014-11-01.openapi.yaml
    cloudwatch-logs/2014-03-28.openapi.yaml
    sfn/2016-11-23.openapi.yaml
  stripe/
    api/2026-09-30.endive.openapi.yaml        # Stripe's whole api, made by tools/stripe-openapi from Stripe's own spec
  openai/
    audio/2026-11-24.openapi.yaml             # OpenAI's two audio uploads, multipart, written by hand
  slack/
    incoming-webhooks/2026-11-23.openapi.yaml # posting to a Slack incoming webhook, written by hand
```

## Rules

1. **Stable.** A file here is only ever fixed, never reshaped. A new version from the vendor is a new file beside the old one.
2. **The vendor's api only.** Nothing that belongs to one customer or one use: no queue URLs, no message schemas, no account IDs. Those go in overlays (short files in the OpenAPI Overlay 1.1 format that change a spec at given spots; see [docs/proxy-overlays.md](../docs/proxy-overlays.md)).
3. **Overlays live with their tests and examples**, never here: in `src/hotline-proxy/testdata/` and in each example's own folder.
4. **Hotline's own marks are included.** The marks the proxy reads, such as `x-hotline-action-header` and `x-hotline-redact`, are part of these files.
5. **Each api's README says where the spec came from** (for SQS: AWS's Smithy model, the file and its commit), how it was made, and on what date.
6. **Every file here passes `hotline-proxy validate`**, checked by a test.
