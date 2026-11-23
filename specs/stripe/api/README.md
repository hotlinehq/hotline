# Stripe api, version 2026-09-30.endive

`2026-09-30.endive.openapi.yaml` is Stripe's whole api (431 paths, 612 calls), as Stripe's own spec gives it, in OpenAPI 3.2. Bodies the app sends are form data (`application/x-www-form-urlencoded`), replies are JSON.

- **Source:** Stripe's spec, `openapi/spec3.yaml` in [stripe/openapi](https://github.com/stripe/openapi) at commit `2dcbcfb9baa2535e672cdd2a0b579cbb940ab4b4` (2026-11-20), version `2026-09-30.endive`, OpenAPI 3.0. A copy is in [tools/stripe-openapi/source](../../../tools/stripe-openapi/source).
- **Made by:** [tools/stripe-openapi](../../../tools/stripe-openapi), never by hand. A test fails when this file differs from what the tool makes from the copy.
  ```
  cd tools/stripe-openapi
  go run . -safe webhook_endpoint -secret key -secret token -secret confirmation_token source/spec3-2026-09-30.endive.yaml > ../../specs/stripe/api/2026-09-30.endive.openapi.yaml
  ```
- **Taken as written** (Peter, 2026-11-23). The tool rewrites only what OpenAPI 3.0 and 3.2 say differently: "may be null" (`nullable: true`, 2,795 places) becomes `null` in the list of types, in the list of values, or a `null` branch of an `anyOf`. Stripe's own `x-` fields are dropped, so every list of allowed values is checked as given. Stripe marks 587 places `x-stripeBypassValidation`, which it does not document and which seems to mean the spec is narrower there than what Stripe accepts (for example `external_account`, written as text, though Stripe also takes a dictionary). A good call reported at one of those places is a reason to revisit this.
- **Path values:** only the webhook endpoint id (`we_...`) is marked `x-hotline-redact: false`, so a record shows which endpoint was read, changed or deleted. Records hide every other path value. The ones that are secrets themselves, the ephemeral key (`key`), the tokens (`token`) and the confirmation token (`confirmation_token`), are marked `x-hotline-redact: true`, since the file under `content: full` hides only path values marked `true`. Other unmarked ids, such as a customer id, appear in that file as sent.
- **Login:** the secret key, sent as `Authorization` (basic or bearer), named in the security section; the proxy never records it.
- **Not hidden under `content: full`:** the `secret` (`whsec_...`) in the reply to creating a webhook endpoint. The file exporter's `content: full` keeps bodies as they are (Peter, 2026-11-23).
