# Slack incoming webhooks

`2026-11-23.openapi.yaml` describes the one call an app makes to a Slack incoming webhook: `POST` a message as JSON to the address Slack gave it, such as `https://hooks.slack.com/services/T000/B000/XXXX`. Slack answers `200` with the text `ok`, or a `4XX` with the reason as plain text. `2026-11-23` is the date it was written; Slack gives incoming webhooks no version.

Written by hand on 2026-11-23, since Slack publishes no OpenAPI file for incoming webhooks, from Slack's own SDKs:

- Slack's Node SDK, `github.com/slackapi/node-slack-sdk` (commit `6815a97`), file `packages/webhook/src/IncomingWebhook.ts`: the message fields (`text`, `blocks`, `attachments`, `unfurl_links`, `unfurl_media`, `metadata`, and `username`, `icon_emoji`, `icon_url`, `channel`, `link_names`) and that the message is sent as JSON.
- Slack's Python SDK, `github.com/slackapi/python-slack-sdk` (commit `2c72986`), file `slack_sdk/webhook/client.py`: the same fields, plus `response_type`, `replace_original` and `delete_original`.
- `thread_ts`, `mrkdwn` and `no_text` (a message with no text, blocks or attachments is refused) come from Slack's incoming webhooks page as known before, and from Slack's `400 no_text` answers quoted in public bug reports (for example GitLab issue 332611). Slack's docs site could not be read from where this was written, so these, and the status code of each refusal, are not checked against it: any `4XX` passes. Check them when the page can be read.

**The address is the secret.** Anyone who has it can post to the channel. Each of the three parts of the path is marked `x-hotline-redact: true`, not just left unmarked: records hide unmarked path values, but the file under `content: full` hides only what is marked `true`. So every record, span, console line and file line shows `https://hooks.slack.com/services/REDACTED/REDACTED/REDACTED`. Give the `hooks.slack.com` host this spec: with no spec, and `urlFull: true` or `content: full`, a minimal record carries the address as sent, secret included, since the proxy then cannot know what is secret.

Only the shape of a message is checked, not Block Kit's own rules for each kind of block: fields Slack adds later pass, since no object here refuses extra fields. The reply is listed as any content type (`*/*`) and is not read: it is a short text, and its content type was not checked against Slack.
