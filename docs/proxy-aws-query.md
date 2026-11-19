# AWS's query form: routes picked by a body field

*Agreed with Peter, 2026-11-01; built the same day. Scenarios: `src/hotline-proxy/features/body-field-routes.feature`, and the shop's use in `aws-shop.feature`. The spec mark is described in [spec-marks.md](spec-marks.md#x-hotline-action-field). Part of step 3 of [the shop example](proxy-shop-example.md).*

## What we checked

- The current AWS SDK for Go v2 still sends SNS and STS in AWS's older "query" form: every call is `POST /` with `Content-Type: application/x-www-form-urlencoded`, and the action is a form field, such as `Action=Publish&Version=2010-03-31&TopicArn=...&Message=...`.
- Responses are XML (`text/xml`), wrapped in the action's name: `<PublishResponse><PublishResult><MessageId>…`. Errors are `<ErrorResponse><Error><Code>…`.
- Lists and maps in the form use dots and numbers starting at 1, such as `MessageAttributes.entry.1.Name=kind`.
- The SDK calls STS at the regional address, `sts.eu-west-1.amazonaws.com`.
- The signature covers the body. The proxy forwards the body byte for byte, so the signature stays valid.

## Decisions

1. **Routes named by a body field.** A mark at the top of our spec, `x-hotline-action-field: Action`, says which body field names the action, like `x-hotline-action-header` does for SQS. Routes are written `/#Action=Publish`. A spec names an action header or an action field, never both. Nothing about AWS is written into the proxy.
2. **When the action is read.** The proxy reads the field from its copy of the request body, at the top level of a form or JSON body, once the body is whole. That one value picks the route for the request check, the response check and the record's `url.template`, so all three agree. The response check waits for the request check to read it.
3. **No action, no route.** A missing field, an unknown action, a body over 2 MiB, or a body that is not form or JSON: the call has no route and is skipped with `no_spec_route`. It is still forwarded unchanged.
4. **The kept body grows from 1 MiB to 2 MiB** (Peter, 2026-11-01). AWS calls can be just over 1 MiB (an SQS message of 1 MiB plus its JSON), and then the body was not checked and fields like `QueueUrl` were missing from the record. 2 MiB fits any valid AWS call. It is a fixed value, not a setting.
5. **A broken spec is refused** at start and by `validate`: routes `#Action=...` with no `x-hotline-action-field`, a mark naming another field, or both marks at once.
6. **Lists and maps written with dots are not checked** (Peter, 2026-11-01: "we need real example to add feature"). The spec maker leaves them out of the form's schema, so correct calls with them are never reported.

## The spec maker (`tools/aws-openapi`)

- **AWS's query form** (SNS, STS): each action's input as form fields, with `Action` and `Version` fixed; plain fields only. Each response is XML with AWS's wrapper, and errors use the `ErrorResponse` shape. `-record-action` marks the `Action` field, `-record` marks other fields.
- **AWS's REST and JSON form** (Lambda, SES v2): routes from the path and the query, values in the path, query and headers as parameters, JSON bodies. A body sent whole, such as a Lambda function's input, is any type, for an overlay to describe.
- Specs made: `specs/aws/sns/2010-03-31`, `specs/aws/sts/2011-06-15`, `specs/aws/lambda/2015-03-31`, `specs/aws/sesv2/2019-09-27`.

## Not done

- SQS's own older query form, sent by SDKs from before late 2023. It now needs only a spec.
- Checking lists and maps in form bodies written with dots.
