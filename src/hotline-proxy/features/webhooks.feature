# Requirements and decisions: docs/decisions/spec-checks.md (M5, 2026-11-23).
Feature: Minimal webhooks, the app calling the vendor
  A webhook call is an ordinary call, checked with the vendor's spec from
  specs/. Two kinds count: setting up webhooks, such as Stripe's webhook
  endpoints, and posting to a vendor's webhook address, such as a Slack
  incoming webhook, whose address is a secret. Calls from the vendor to the
  app are out of scope (Peter, 2026-11-21).

  Rule: Setting up Stripe webhook endpoints is checked against Stripe's spec

    Scenario: Creating a webhook endpoint passes
      Given the Stripe spec from specs, with Stripe answering a new webhook endpoint
      When the app posts to Stripe's /v1/webhook_endpoints the form "url=https%3A%2F%2Fshop.example%2Fhooks&enabled_events[0]=invoice.paid"
      Then the record says hotline.schema.status=valid
      And url.template is /v1/webhook_endpoints

    Scenario: A webhook endpoint without its url is reported
      Given the Stripe spec from specs, with Stripe answering a new webhook endpoint
      When the app posts to Stripe's /v1/webhook_endpoints the form "enabled_events[0]=invoice.paid"
      Then the request results are body $.url required

    Scenario: An event name not in Stripe's list is reported, as the spec is taken as written
      Stripe marks this list x-stripeBypassValidation, which the tool drops
      (Peter, 2026-11-23, Q3).
      Given the Stripe spec from specs, with Stripe answering a new webhook endpoint
      When the app posts to Stripe's /v1/webhook_endpoints the form "url=https%3A%2F%2Fshop.example%2Fhooks&enabled_events[0]=invoice.paied"
      Then the one request result is body $.enabled_events[0] enum, with Stripe's 269 event names

    Scenario: The webhook endpoint id is kept in the address
      Given the Stripe spec from specs, with Stripe answering a deleted webhook endpoint
      When the app deletes Stripe's /v1/webhook_endpoints/we_1Mr5
      Then url.full is "https://api.stripe.com/v1/webhook_endpoints/we_1Mr5"
      And the record says hotline.schema.status=valid

    Scenario: Every other Stripe path value stays hidden
      Given the Stripe spec from specs, with Stripe answering a deleted webhook endpoint
      When the app deletes Stripe's /v1/customers/cus_1Mr5
      Then url.full is "https://api.stripe.com/v1/customers/REDACTED"

    Scenario: A secret Stripe path value stays hidden under content full too
      The file hides only path values marked x-hotline-redact true, so the
      tool marks the ephemeral key, the tokens and the confirmation token.
      Given the Stripe spec from specs, with Stripe answering a deleted webhook endpoint, content full and a file exporter
      When the app deletes Stripe's /v1/ephemeral_keys/ephkey_1Mr5
      Then url.full is "https://api.stripe.com/v1/ephemeral_keys/REDACTED"
      And the line's hotline.content.url.full is "https://api.stripe.com/v1/ephemeral_keys/REDACTED"

  Rule: Posting to a Slack incoming webhook is checked, and its address never shown

    Scenario: A message posted to a Slack incoming webhook passes
      Slack answers with a short text, which the proxy does not read, so
      the check of the reply is skipped.
      Given the Slack spec from specs, with Slack answering 200 "ok"
      When the app posts the message {"text":"Order A-1001 paid"} to its Slack webhook
      Then the record has no request results
      And the record has no response results
      And the record says hotline.schema.skipped=unsupported_content_type
      And url.template is /services/{team}/{bot}/{token}

    Scenario: A message with no text, blocks or attachments is reported
      Given the Slack spec from specs, with Slack answering 400 "no_text"
      When the app posts the message {"thread_ts":"1700000000.000200"} to its Slack webhook
      Then the request results are body $ anyOf

    Scenario: Slack's refusal passes the status check
      Given the Slack spec from specs, with Slack answering 403 "invalid_token"
      When the app posts the message {"text":"hi"} to its Slack webhook
      Then the record has no request results
      And the record has no response results

    Scenario: The secret in the webhook address is never shown, even under content full
      Given the Slack spec from specs, with Slack answering 200 "ok", content full, a file exporter and a console exporter
      When the app posts the message {"text":"hi"} to its Slack webhook
      Then url.full is "https://hooks.slack.com/services/REDACTED/REDACTED/REDACTED"
      And the line's hotline.content.url.full is "https://hooks.slack.com/services/REDACTED/REDACTED/REDACTED"
      And no record, span, log, console line or file line holds any part of the webhook address
