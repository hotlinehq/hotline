# stripe-openapi

Writes `specs/stripe/api/<version>.openapi.yaml` from Stripe's own OpenAPI 3.0 spec, kept in `source/`. See [specs/stripe/api/README.md](../../specs/stripe/api/README.md) for what it rewrites and why.

```
go run . -safe webhook_endpoint -secret key -secret token -secret confirmation_token source/spec3-2026-09-30.endive.yaml > ../../specs/stripe/api/2026-09-30.endive.openapi.yaml
```

A new Stripe version is a new file in `source/`, named after Stripe's version, and a new spec beside the old one. A nullable the tool cannot rewrite stops it with the line number, rather than being guessed.
