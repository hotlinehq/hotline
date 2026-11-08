# Checking calls against the vendor's OpenAPI spec: requirements

*Agreed with Peter, 2026-10-29. Built in two steps: the shared apis in the config (decisions 10 and 11), then the checks. Scenarios: `src/hotline-proxy/features/checks.feature`.*

## What the feature is

The proxy checks each call against our copy of the vendor's OpenAPI spec (the vendor's published description of its API). Peter's point (2026-10-29): a check is not only about the JSON body. For each route the spec describes every part of the request, and says which parts are required:

- **path values**, like `{sessionId}` in `/v1/customer_sessions/{sessionId}`;
- **query values**, like `?page=2`;
- **headers**, like `Idempotency-Key`;
- **cookies** (rare in vendor APIs);
- **the body**, in JSON or form data, with its content type.

All of these take part in the check. The reply is checked the same way: its status code, its headers and its body.

## Already settled by earlier decisions

- **Both ways.** Requests and replies are checked (spec section 1; capabilities review item 15).
- **Never in the way.** The call streams through untouched. Each side is checked as soon as it is whole, alongside the call: the request once it is sent, the reply once it has arrived. The app never waits for them, and they never block, change or refuse a call (Peter, 2026-10-31). The proxy only reports; the control plane judges.
- **Results name the place and the rule, never the value.** They go in `hotline.schema.request.errors` and `hotline.schema.response.errors`, for example `$.amount required missing`.
- **Skipped checks say why**, in `hotline.schema.skipped`: `body_too_large` (over 2 MiB; 1 MiB until 2026-11-01), `no_spec_route`, `streaming`, `unsupported_content_type`, `overloaded`.
- **Body types.** JSON and form data (`application/x-www-form-urlencoded`, as Stripe uses) are checked. Anything else is skipped with `unsupported_content_type`.
- **Who gets checked.** Only calls with a known integration and a host in its `apis`. `not-passed` and unknown IDs get a minimal record and no checks.
- **Spec versions.** Our example specs are OpenAPI 3.1.

## Open questions

None. All nine were answered; see the decisions below.

## Decisions

1. **Extra values not in the spec** (Peter, 2026-10-29). A query value the spec doesn't name is reported. A header the spec doesn't name is ignored, since apps and their libraries add many of their own. Extra body fields follow the schema's own rule (`additionalProperties`).
2. **How a result names the part** (Peter, 2026-10-29; made structured the same day). Each result is a small structured entry, not a sentence, so nobody has to parse text later:
   - `part`: `path`, `query`, `header`, `cookie`, `body` or `status`;
   - `place`: the name (`page`, `Idempotency-Key`) or, in a body, where in it (`$.cartItems[0].quantity`); none for `status`;
   - `rule`: one word from a fixed list, using JSON Schema's own keyword names where one exists: `required`, `type`, `format`, `minimum`, `maximum`, `enum`, `pattern`, `not_in_spec`, `content_type`, and so on;
   - `expected`: what the spec asks for (`integer`, `1`, `uuid`). It comes from the spec, never from the call.
   Never values from the call. In OpenTelemetry the list is a list of key and value maps, which log records allow.
3. **A body type the spec doesn't list** (Peter, 2026-10-29). A content type the spec doesn't list for the route is reported (`part: body`, `rule: content_type`), and so is a body where the spec expects none (`part: body`, `rule: not_in_spec`). The skip reason `unsupported_content_type` stays only for formats the spec lists but the proxy can't read, like XML or file uploads.
4. **A reply status the spec doesn't list** (Peter, 2026-10-29). With no `default` reply in the spec, it is reported as `{part: status, rule: not_in_spec}`, and the reply's headers and body are not checked, since there is no schema for them. A `default` reply matches any status.
5. **A limit on results** (Peter, 2026-10-29). At most 20 results for the request and 20 for the reply. When there are more, the record also gives the total, in `hotline.schema.request.errors_total` and `hotline.schema.response.errors_total`.
6. **Formats are checked** (Peter, 2026-10-29). The common, clearly defined formats are checked as rules: `date-time`, `date`, `email`, `uuid`, `uri`, `ipv4`, `ipv6`, `int32` and `int64`. Other formats are ignored. A miss is `{rule: format, expected: uuid}`.
7. **The login key must be present** (Peter, 2026-10-29). When the spec's security section asks for a key or token, the proxy checks only that it is there, and reports a missing one as `{part: header, place: Authorization, rule: required}` (or the query or cookie it lives in). The value is never read, checked or recorded.
8. **Spec features in the first version** (Peter, 2026-10-29). Only OpenAPI 3.1; a 3.0 spec is refused when loaded. Shared parts (`$ref`) only within the same file. Lists and objects in query values in the usual OpenAPI ways (`?tag=a&tag=b`, `?filter[status]=open`). Stripe's form bodies (`metadata[order]=…`, `items[0][price]=…`) turned into a JSON-like shape for the schema. Callbacks, webhooks and XML come later.
9. **A broken spec is a config problem** (Peter, 2026-10-29). A schema the proxy can't use (a wrong type name, a `$ref` that points nowhere, a link to another file, an OpenAPI version other than 3.1) stops the proxy at start, is listed by `hotline-proxy validate`, and on a later reload keeps the old config.
10. **One spec, shared by all integrations** (Peter, 2026-10-29). Many integrations can use the same vendor spec, for example hundreds of integrations all calling Talon.One. A spec file is read, checked and turned into ready-to-use schemas once, and every integration that names it uses that one copy. Memory and start time grow with the number of different spec files, not with the number of integrations. The loader already reads each spec file once by its path; the schemas built for checks follow the same rule.
11. **Each vendor api is named once in the config** (Peter, 2026-10-29). The full example is `src/hotline-proxy/testdata/proxy-example/hotline-proxy.yaml`, a copy of `proxy-example/hotline-proxy.yaml` in the `hotline-strategic` repo.
    ```yaml
    apis:                              # each vendor api, once
      talon-one:
        hosts: [api.talon.one]
        spec: ./specs/talon-one.openapi.yaml
        vendorTimeout: 10s
      stripe:
        hosts: [api.stripe.com]
        spec: ./specs/stripe.openapi.yaml

    integrations:
      - id: talon-shop-a
        fingerprintKey: { file: /etc/hotline/keys/talon-shop-a }
        apis: [talon-one, stripe]      # names from the apis section
      - id: talon-shop-b
        apis:
          - { api: talon-one, vendorTimeout: 5s }   # an integration may change only vendorTimeout
      - id: not-passed                 # calls with no ID or an unknown ID; optional
        # no apis: these settings apply to any host, and these calls are never checked
        tlsOrigination: false          # built-in default true since 2026-11-01
        vendorTimeout: 30s             # built-in default 60s
        allowInternalIPs: false        # built-in default false
    ```
    - `hosts`, `spec`, `tlsOrigination` and `allowInternalIPs` belong to the api; an integration may change only `vendorTimeout`.
    - An integration naming an api that doesn't exist, or two of its apis sharing a host, is a config problem: refused at start and by `validate`.
    - `not-passed` stays an entry under `integrations`, with its settings written on itself, since it has no apis. An `apis` list on it is ignored with a warning, as today.
    - The old form (hosts and spec written inside each integration) is dropped, since nothing has been released yet.

## Details settled while building

- A JSON body that cannot be read is reported as `{part: body, place: $, rule: json}`; a form body with a broken escape as `rule: form`.
- Checks use the JSON Schema library `santhosh-tekuri/jsonschema` (Apache-2.0), which follows the JSON Schema version OpenAPI 3.1 uses. Formats other than the agreed list are switched off in it.
- **Loading a large spec stays cheap** (Peter, 2026-11-01). The JSON Schema library copies all it has read each time it builds a check for a new place in the spec, so one check per call made loading grow with the calls times the shared shapes: the SSM spec (152 calls, 1,189 shapes) took 1.3 seconds and about 950 MB of short-lived memory. Now a schema that is only a `$ref` within the spec uses the check already built for the shape it names, and a schema that needs nothing else from the spec, such as `{ type: string }` on a header, is built apart from the spec, once for each distinct text. SSM now takes about 0.5 seconds and 300 MB, and keeps 4.8 MB instead of 6.3 MB. The vendor spec files are unchanged. The SSM spec is the size test in `internal/spec/load_test.go`.
- **Checks wait in a line capped by memory** (Peter, 2026-11-02, found by the load test: large replies at about 450 a second filled every core with checks, and every call then waited about a second). Checks run on one worker per processor core the proxy may use. A check that finds every worker busy waits in a line, holding its body copy in memory, never on disk. `specChecks.queueMemory` caps the memory the waiting copies take: plain bytes or `KiB`, `MiB`, `GiB`, at least 1 byte, 64 MiB unless set, and a reload may change it. When a new check does not fit, the oldest waiting checks that hold a body give way: each is skipped as `overloaded`, and its record goes out at once. A body larger than the whole cap is skipped when no worker is free. A free worker takes a check of any size.
  - One line and one cap for requests and replies. A reply check that needs the route from the request's body joins the line once the request's check has ended.
  - Traces show the wait as `wait for check worker` under each spec check; a skipped one is an error with `error.type=overloaded`. Own telemetry: `hotline.spec_check.queue.memory.usage`, `hotline.spec_check.queue.size`, `hotline.spec_check.dropped`.
  - When the time to stop runs out, checks still waiting are skipped as `overloaded`, and the proxy logs how many (`stop ran out of time, skipped the spec checks still waiting`).

## Which parts run (Peter, 2026-11-08)

`checks` on an api picks the parts to check: `path`, `query`, `headers`, `cookies`, `body` for the request, and `status`, `reply-headers`, `reply-body` for the reply. Finding the route and its template always happens. Leaving `checks` out checks every part; `checks: []` checks none, so the record has the route template but no `hotline.schema.*` attributes and the trace has no spec check spans. An unknown name stops the start. A part left out leaves no result and no skipped reason. A body copy is kept only when `body` (request) or `reply-body` is picked; the one exception is a spec that names the route's action in the request body (AWS), which needs that body to find the route. It applies on every ingress.

```yaml
apis:
  stripe:
    hosts: [api.stripe.com]
    spec: ./specs/stripe.yaml
    checks: [path, query, headers, status]   # no bodies
```

## What changes in the proxy spec

- Section 1: "JSON checks" becomes checks of every part of the call (path, query, headers, cookies, body; and the reply's status, headers and body).
- Section 4.1: `hotline.schema.request.errors` and `hotline.schema.response.errors` become lists of structured entries (`part`, `place`, `rule`, `expected`), at most 20 each, plus the new `hotline.schema.request.errors_total` and `hotline.schema.response.errors_total`.
- The example record in `proxy-example/README.md` changes to the structured form.
- Section 2: the config gains the top-level `apis` section, and integrations name their apis (decision 11). The full example in `proxy-example/hotline-proxy.yaml` changes to match.
- The example Stripe spec gains a route with a form body (`POST /v1/payment_intents`), so form checks can be shown and tested.
