# Edge proxy roadmap: OpenAPI 3.2

Draft, 2026-11-21. Planning only, nothing built. Built from [support-list.md](support-list.md), which comes from the gaps in [gaps.md](gaps.md). Each milestone is one or more slices, built tests first, and each needs Peter's go-ahead on its requirements before any code.

Sizes are rough guesses, not measured: **small** is about one pull request, **medium** two or three, **large** more than three.

## M0. Move to 3.2 only (small, built 2026-11-21)

- Checked the 3.2.0 spec text against the support list: every item is in it, and 3.2 lists no breaking change from 3.1. XML `attribute` and `wrapped` are deprecated (still allowed), not removed.
- New 3.2 parts the proxy can't check yet load and are skipped quietly (Peter, 2026-11-21).
- The proxy loads `openapi: 3.2.*` and refuses everything else, with a message saying to change the version.
- Rewrite every spec in `specs/` and every test spec to 3.2.
- A spec with `webhooks` or `callbacks` sections still loads, and those sections are ignored (see M5).
- Depends on: nothing. Everything below depends on it.

## M1. Stop silent mistakes (small, built 2026-11-21)

These mistakes give no warning today, so they come before new features.
- A server address with a variable in the host, like `{region}`, quietly dropped the base path. Built: the host is no longer parsed, since the config names the hosts, so the base path stays.
- AsyncAPI traits were ignored. Built: they refuse the spec for now; merging them in comes later if a vendor needs it.
- A message `schemaFormat` other than JSON Schema (Avro or Protobuf, for example) was ignored. Built: it refuses the spec. AsyncAPI's, OpenAPI's and JSON Schema's own formats load.
- Second sweep (built 2026-11-23): parameters described with `content`, the `matrix`, `label`, `spaceDelimited` and `pipeDelimited` styles, and every server's base path were skipped or read wrongly. They are now checked; a spec is refused only where OpenAPI forbids what it says ([decisions/spec-checks.md](../decisions/spec-checks.md), items 30 to 34).
- Depends on: M0.

## M2. Streamed replies with `itemSchema` (large, the biggest win)

- Check each Server-Sent Event, each JSON Lines line and each JSON sequence item against 3.2's `itemSchema`. This covers streamed replies from OpenAI and Anthropic.
- New work: the proxy now keeps a copy of at most 2 MiB of a body and skips streams. It would have to check items one at a time as they pass, without holding the whole stream and without slowing the stream down.
- Recording: decided, like WebSocket: a record and a span per item, and a stream record at the end ([decisions/streamed-responses.md](../decisions/streamed-responses.md)).
- Built: Server-Sent Events, checked against a hand-written Anthropic spec, then JSON Lines and JSON sequences, and the shop's `support` app, whose streamed Claude responses are checked in the Floci workflow. M2 is done.
- Depends on: M0.

## M3. Small 3.2 additions (small to medium)

- `QUERY` method, and other methods under `additionalOperations`.
- `in: querystring`: the whole query checked as one value.
- Cookie style for cookie parameters.
- Server `name`, to label which server a call went to.
- `discriminator` with `defaultMapping`: errors name the right branch.
- Built (part 1, 2026-11-22): `QUERY`, `additionalOperations` and `discriminator`, with Anthropic's content blocks and stream deltas written with a discriminator.
- Built (part 2, 2026-11-23): `in: querystring` and cookie styles. Server `name` is left for later: no spec uses it yet.
- Depends on: M0. Can run before or after M2.

## M4. Multipart bodies (medium, built 2026-11-24)

- Check the parts that are not files, using `encoding`, and 3.2's `itemEncoding` and `prefixEncoding`. File parts are counted, never read.
- Built (2026-11-24): `multipart/form-data` request bodies, read part by part as they pass, with OpenAI's audio uploads and Stripe's file upload. `multipart/mixed`, `itemEncoding` and `prefixEncoding` are left out: no spec uses them. Decisions 41 to 49 in [decisions/spec-checks.md](../decisions/spec-checks.md). M4 is done.
- Depends on: M0. Shares the item-by-item reading from M2 if `multipart/mixed` streams are wanted.

## M5. Webhooks, minimal: the app calling the vendor (medium, built 2026-11-23)

Decided (Peter, 2026-11-21): only the part where the app calls the vendor. Calls from the vendor to the app are out of scope. Two kinds of call count:

- **Setting up webhooks.** The app tells the vendor where to send events, for example Stripe's `POST /v1/webhook_endpoints` or GitHub's `POST /repos/{owner}/{repo}/hooks`. These are ordinary paths, checked like any other call. The work is to add them to our vendor specs and test them, so we know they check correctly.
- **Sending to a vendor's webhook address.** The app posts a message to an address the vendor gave it, for example a Slack or Discord incoming webhook (`https://hooks.slack.com/services/T000/B000/XXXX`). The address holds a secret, so the record must never show it. Path values are already hidden unless marked safe; a test keeps that true for these addresses. The message body is checked against the vendor's schema.
- **Not checked:** the spec's `webhooks` and `callbacks` sections. They describe calls the vendor makes to the app, so they are loaded and ignored.
- First vendors: Stripe and Slack (Peter, 2026-11-23).
- Built (2026-11-23): Stripe's whole spec, made by `tools/stripe-openapi` from Stripe's own and taken as written, and a hand-written Slack incoming webhooks spec whose address is hidden everywhere, `content: full` included. Decisions 35 to 40 in [decisions/spec-checks.md](../decisions/spec-checks.md). M5 is done.
- Depends on: M0.

## Later: only when a real example needs it

Calls from a vendor to the app (webhooks, `callbacks`) are out of scope.

Rarer parameter styles (`matrix`, `label`, `spaceDelimited`, `pipeDelimited`), parameters described with `content`, XML `nodeType`, non-JSON WebSocket messages, other AsyncAPI protocols (Kafka, MQTT) and standalone AsyncAPI files.

## Order at a glance

M0, then M1, then M2. M3, M4 and M5 can slot in around M2; M5 is the smallest.
