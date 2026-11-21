# Edge proxy roadmap: OpenAPI 3.2

Draft, 2026-11-21. Planning only, nothing built. Built from [support-list.md](support-list.md), which comes from the gaps in [gaps.md](gaps.md). Each milestone is one or more slices, built tests first, and each needs Peter's go-ahead on its requirements before any code.

Sizes are rough guesses, not measured: **small** is about one pull request, **medium** two or three, **large** more than three.

## M0. Move to 3.2 only (small)

- Check the 3.2 spec text against the support list; my list of what 3.2 adds came from memory.
- The proxy loads `openapi: 3.2.*` and refuses everything else, with a message saying to change the version.
- Rewrite every spec in `specs/` and every test spec to 3.2.
- A spec with `webhooks` or `callbacks` sections still loads, and those sections are ignored (see M5).
- Depends on: nothing. Everything below depends on it.

## M1. Stop silent mistakes (small)

These mistakes give no warning today, so they come before new features.
- A server address with a variable in the host, like `{region}`, quietly drops the base path. Refuse it, or read the variables.
- AsyncAPI traits are ignored. Refuse them for now; merging them in comes later if a vendor needs it.
- A message `schemaFormat` other than JSON Schema (Avro or Protobuf, for example) is ignored. Refuse it.
- Depends on: M0.

## M2. Streamed replies with `itemSchema` (large, the biggest win)

- Check each Server-Sent Event, each JSON Lines line and each JSON sequence item against 3.2's `itemSchema`. This covers streamed replies from OpenAI and Anthropic.
- New work: the proxy now keeps a copy of at most 2 MiB of a body and skips streams. It would have to check items one at a time as they pass, without holding the whole stream and without slowing the stream down.
- Recording: decide what a record holds for a stream, such as an item count and the first broken item. That's a question for Peter.
- Depends on: M0.

## M3. Small 3.2 additions (small to medium)

- `QUERY` method, and other methods under `additionalOperations`.
- `in: querystring`: the whole query checked as one value.
- Cookie style for cookie parameters.
- Server `name`, to label which server a call went to.
- `discriminator` with `defaultMapping`: errors name the right branch.
- Depends on: M0. Can run before or after M2.

## M4. Multipart bodies (medium)

- Check the parts that are not files, using `encoding`, and 3.2's `itemEncoding` and `prefixEncoding`. File parts are counted, never read.
- Depends on: M0. Shares the item-by-item reading from M2 if `multipart/mixed` streams are wanted.

## M5. Webhooks, minimal: the app calling the vendor (small)

Decided (Peter, 2026-11-21): only the part where the app calls the vendor. Calls from the vendor to the app are out of scope. Two kinds of call count:

- **Setting up webhooks.** The app tells the vendor where to send events, for example Stripe's `POST /v1/webhook_endpoints` or GitHub's `POST /repos/{owner}/{repo}/hooks`. These are ordinary paths, checked like any other call. The work is to add them to our vendor specs and test them, so we know they check correctly.
- **Sending to a vendor's webhook address.** The app posts a message to an address the vendor gave it, for example a Slack or Discord incoming webhook (`https://hooks.slack.com/services/T000/B000/XXXX`). The address holds a secret, so the record must never show it. Path values are already hidden unless marked safe; a test keeps that true for these addresses. The message body is checked against the vendor's schema.
- **Not checked:** the spec's `webhooks` and `callbacks` sections. They describe calls the vendor makes to the app, so they are loaded and ignored.
- Open: which vendors first. Slack incoming webhooks and Stripe's webhook endpoints are my picks, since both are common and both have public specs.
- Depends on: M0.

## Later: only when a real example needs it

Calls from a vendor to the app (webhooks, `callbacks`) are out of scope.

Rarer parameter styles (`matrix`, `label`, `spaceDelimited`, `pipeDelimited`), parameters described with `content`, XML `nodeType`, non-JSON WebSocket messages, other AsyncAPI protocols (Kafka, MQTT) and standalone AsyncAPI files.

## Order at a glance

M0, then M1, then M2. M3, M4 and M5 can slot in around M2; M5 is the smallest.
