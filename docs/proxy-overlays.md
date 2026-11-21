# Overlays on an api's spec: requirements draft

*Agreed with Peter, 2026-10-30, and built. Scenarios: `src/hotline-proxy/features/overlays.feature` and `overlay-command.feature`. It comes before the SQS example ([proxy-sqs-example.md](proxy-sqs-example.md), pull request #33), which needs it. His answers are at the end.*

## Why

The SQS example has three integrations. All three use the same SQS spec, but each one has its own queue and its own message schema. Copying the whole SQS spec three times would mean three big files to keep in step. Instead, each api keeps the shared spec and adds a small **overlay**.

An overlay is a short file in the official OpenAPI Overlay format. It is a list of actions, and each action says "at this spot in the spec, change this". The spot is found with JSONPath, a small query language for picking parts of a JSON or YAML document (written down in RFC 9535). The proxy reads the latest version of the format, 1.1 (January 2026).

An example overlay for the invoices queue:

```yaml
overlay: 1.1.0
info: { title: Invoices queue, version: "1" }
actions:
  - target: $.components.schemas.SendMessageRequest.properties.QueueUrl
    update: { const: https://sqs.eu-west-1.amazonaws.com/192325781350/invoices }
  - target: $.components.schemas.SendMessageRequest.properties.MessageBody
    update:
      contentMediaType: application/json
      contentSchema: { $ref: '#/components/schemas/InvoiceJob' }
  - target: $.components.schemas
    update:
      InvoiceJob: { type: object, required: [orderId, templateId] }
```

## The config

Each api gets an optional `overlays` list, next to its `spec`:

```yaml
apis:
  aws-sqs-invoices:
    hosts: [sqs.eu-west-1.amazonaws.com]
    spec: ./specs/aws-sqs.openapi.yaml
    overlays: [./specs/aws-sqs-invoices.overlay.yaml]
  aws-sqs-refunds:
    hosts: [sqs.eu-west-1.amazonaws.com]
    spec: ./specs/aws-sqs.openapi.yaml
    overlays: [./specs/aws-sqs-refunds.overlay.yaml]
```

## Requirements

1. **Overlays belong to the api.** `overlays` is a list of file paths on an api in the `apis` section, read the same way as `spec`. An integration cannot add overlays (decision 11 of the spec checks: an integration may change only `vendorTimeout`).
2. **Applied once, at load.** When the proxy reads the config, it reads the spec, applies the overlays, and then treats the result exactly like a spec written by hand: every rule for specs applies to it (OpenAPI 3.2 only, checked after the overlays are applied, `$ref` within the same file, the marks such as `x-hotline-redact` and `x-hotline-action-header`). Calls are checked only against this merged spec.
3. **Order.** The overlays are applied in the order the list gives. Inside one file, the actions run in the order written, each on the result of the one before, as the format says.
4. **Only the latest version, 1.1** (Peter, 2026-10-30). Files with `overlay: 1.1.x` are read; the last number only marks fixes to the format's text. Actions `update`, `remove` and `copy` work as the format says. Any other version, such as `1.0.0` or `2.0.0`, is refused. Moving a 1.0 file to 1.1 is usually just changing that number.
5. **Standard JSONPath only.** Targets are read as RFC 9535 JSONPath, as 1.1 says. Tool-specific extras are not accepted.
6. **A target that matches nothing is a warning** (Peter, 2026-10-30). The action does nothing, as the format says, and the proxy still starts. It logs one warning line at start naming the api, the overlay file and the action's number, and `validate` lists the same warning. Such an overlay is usually out of date, so the warning makes a check that quietly stopped easy to spot. The same holds for `remove`. A `copy` whose source matches nothing is an error, since there is nothing to copy.
7. **A broken overlay is a config problem**, like a broken spec: refused at start, listed by `hotline-proxy validate`, and on a later reload the old config stays. Broken means: a file that can't be read, isn't YAML or JSON, or isn't an overlay (no `overlay`, `info` or `actions`); a target that isn't valid JSONPath; a `copy` whose source matches nothing or more than one spot; an `update` of the wrong kind for its spot (a list onto an object, for example); or a merged spec that breaks a spec rule. Each problem names the api, the overlay file and the action's number, for example `api aws-sqs-invoices: overlay ./specs/aws-sqs-invoices.overlay.yaml, action 2: target $.components.schemas.Nope matches nothing`.
8. **`extends` is ignored.** An overlay file may name the spec it was written for in `extends`. The config already says which spec the overlay goes on, so the proxy does not read `extends`.
9. **Sharing stays cheap.** The base spec file is read once, however many apis use it. Apis with the same spec and the same overlay list in the same order share one merged spec, built once. Memory now grows with the number of different (spec, overlays) pairs, not with the number of integrations.
10. **Records don't change.** `url.template`, the check results and their `place` all come from the merged spec. No new record field: `hotline.config.version` holds a fingerprint of the config, its specs and overlays, so changing an overlay file changes it.
11. **Reload.** Overlays are config: whenever the config is read again (the reload slice, still to come), its overlay files are read again with it.

## Making an overlay from two specs (Peter, 2026-10-30)

Peter asked for a way to make an overlay from two specs. A new command of the program:

```
hotline-proxy overlay --from ./specs/aws-sqs.openapi.yaml --to ./specs/aws-sqs-invoices.openapi.yaml --title "Invoices queue" > ./specs/aws-sqs-invoices.overlay.yaml
```

1. It writes an overlay (version 1.1) to standard output that turns the `--from` spec into the `--to` spec. `--title` goes into `info.title` (`Hotline overlay` when not given), and `info.version` is `1`.
2. The result is checked before it is written: applying it to `--from` must give the same values as `--to` (the order of keys does not matter). If not, the command writes nothing and exits with 1. Two specs with the same values give no overlay, also exit 1.
3. A file that can't be read or isn't YAML or JSON exits with 1 and a clear message, as `validate` does. A wrong command line exits with 2.
4. The proxy itself is not involved: no config, no listening. It is a helper for whoever writes the overlays.

The chosen library already has this (its `Compare`). I tried it on the SQS case: the overlay it made, applied back to the plain spec, gave the changed spec exactly. Its output says version 1.0 with a tool-specific line, so the command rewrites it as 1.1 and drops that line.

## Library

Chosen by Peter, 2026-10-30: [`github.com/speakeasy-api/openapi/overlay`](https://pkg.go.dev/github.com/speakeasy-api/openapi/overlay), MIT license. It supports 1.1 with `copy`, lists each target that matches nothing (which the proxy turns into warnings), and works on `go.yaml.in/yaml/v3` nodes, the same YAML library the proxy already uses; it also uses the same JSON Schema library. I tried it on the SQS case above: the three updates landed where expected, and a target that matched nothing was named clearly. One thing to handle on our side: its own docs still call the overlay part early ("alpha"), so our tests cover each rule above.

The other choices: `pb33f/libopenapi` (also supports 1.1, but brings its own YAML library and a whole OpenAPI model we don't use), or our own small code on top of a JSONPath library (more code to own, for no gain).

## What changes in the proxy spec

Section 2 (config file): the `apis` example gains `overlays`, and a new rule says what overlays are and requirements 2, 3, 6 and 7 in short. Proposed in a pull request to `hotline-strategic`.

## Scenarios

The behaviour is written down and tested in `src/hotline-proxy/features/overlays.feature` (overlays in the config) and `src/hotline-proxy/features/overlay-command.feature` (the `overlay` command, and warnings in `validate`, run against the built program).

## Answers (Peter, 2026-10-30)

1. A target that matches nothing: a warning, not a refusal (requirement 6).
2. Overlays on the api only (requirement 1).
3. Library: `speakeasy-api/openapi/overlay`.
4. Version: only the latest, 1.1 (requirement 4).

## Not in this step

- Overlays fetched from a web address; only local files.
- Overlays written inside the config file itself.
- A command that prints the merged spec. Easy to add later if debugging an overlay needs it.
