# Writing every call to a local file

A file exporter writes each call the proxy sees to a file on the local disk, one line per call. It is for local use: trying Hotline on a laptop without a collector, or reading exactly what an app sent and got back. All of it was agreed with Peter on 2026-11-11; the behaviour is written down in `src/hotline-proxy/features/file-export.feature`.

## Config

```yaml
exporters:
  - file:
      path: ./calls          # a folder; a relative path counts from the config file's folder
      maxFileSize: 100MiB    # start a new file past this size (default 100MiB)
      maxFiles: 10           # rotated files kept; older ones are deleted (default 10)
      compression: gzip      # gzip (default) or none, for rotated files only
      queueMemory: 256MiB    # most memory the calls waiting to be written may hold (default 256MiB)

integrations:
  - id: checkout
    content: full            # records (default) or full: every header and the bodies, for file exporters only
    maxBodySize: 1MiB        # with full: how much of each body is kept (default 1MiB)
```

A file exporter may stand alone or next to `otlp` exporters. Several file exporters are allowed, each in its own folder.

## The file

The folder holds `calls.jsonl`, one line of JSON per call, and the rotated files. A WebSocket session that ended also gets a line, with `event` set to `hotline.websocket.session` and the session record's fields ([otel-export.md](otel-export.md), Peter, 2026-11-18). There is no file for metrics or traces: the file exporter writes no metrics, and a call's spans are inside its line.

A line looks like this, spread out here to read:

```json
{
  "time": "2026-11-11T07:36:04.000000Z",
  "severity": "INFO",
  "event": "hotline.request",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7",
  "resource": { "service.name": "hotline-proxy", "service.version": "…", "service.instance.id": "…" },
  "attributes": {
    "hotline.integration.id": "checkout",
    "http.request.method": "POST",
    "http.response.status_code": 201,
    "hotline.trace.spans": [
      { "span_id": "00f067aa0ba902b7", "parent_span_id": null, "name": "hotline call", "kind": "server",
        "start_time": "…", "end_time": "…", "status": "unset" },
      { "span_id": "a2fb4a1d1a96d312", "parent_span_id": "00f067aa0ba902b7", "name": "vendor call", "kind": "client",
        "start_time": "…", "end_time": "…", "status": "unset" }
    ]
  }
}
```

- The attributes are the record every exporter gets, with the same names ([otel-export.md](otel-export.md)): numbers are numbers, header values are lists of text.
- The names of the line's own fields follow OpenTelemetry's data model for logs and spans. The line is not OTLP's own JSON (`resourceLogs` and so on), so the OpenTelemetry Collector cannot read the file back; the `otlp` exporters are the way to a collector.
- A probe's line (`hotline.probe`) also has `body`, the verdict as a sentence, and its root span has `links`, each with `trace_id` and `span_id`: the failed call that made the probe due ([decisions/probe.md](decisions/probe.md)).
- A line shows up at most 100 ms after the call's record is made, or sooner once 512 wait. The record is made once the response has gone back to the app and the spec checks have ended, as for every exporter.

To follow calls as they come: `tail -f calls/calls.jsonl | jq .`

## Rotation

When the next line would take `calls.jsonl` past `maxFileSize`, the file is renamed to `calls-<UTC time>.jsonl`, gzipped to `calls-<UTC time>.jsonl.gz` (unless `compression: none`), and a new `calls.jsonl` starts. Past `maxFiles` rotated files, the oldest is deleted. A line is never split across files, and the current file is never compressed, so it can be read and followed at any time.

## Full content

`content: full` on an integration (or on the `not-passed` entry, for calls without a known ID) makes the file exporters get that integration's calls in full:

Full content goes beside the record, under `hotline.content.*`, and the record's own attributes stay as they are (Peter, 2026-11-16):

- **Every header** of the call and the response, in `hotline.content.request.header.<name>` and `hotline.content.response.header.<name>`, as sent unless it is secret (below). Other `proxy-` headers are kept as sent.
- **The bodies**, in `hotline.content.request.body` and `hotline.content.response.body`, as base64 of the bytes as they passed: a gzipped body stays gzipped. Each is cut at `maxBodySize`; a cut body has `hotline.content.request.body.truncated` or `hotline.content.response.body.truncated` set to true, and `http.request.body.size` and `http.response.body.size` still hold the real sizes.
- **`hotline.content.url.full`** is the address as sent, but for secret query words and path values (below).
- **Secrets are `REDACTED`** (Peter, 2026-11-18), so nobody copies a vendor key out of a file kept for debugging. Secret, always: `Authorization`, `Proxy-Authorization`, `Cookie` and `Set-Cookie`. With a spec, also the login its security section names (a header such as `X-Api-Key`, or a query word such as `key`), and every header, query word and path value marked `x-hotline-redact: true`. A value the spec does not call secret, marked or not, stays as sent: `content: full` keeps far more than the record. Until 2026-11-18 only `Proxy-Authorization` was hidden.

To read a body: `jq -r '.attributes."hotline.content.request.body" | @base64d' calls/calls.jsonl`. For a binary body, take the value out with `jq -r` and decode it with `base64 -d`.

Beyond the secrets above, nothing is redacted: tokens the spec does not name, passwords in bodies and personal data land in the file. The proxy logs a warning at start for each integration with `content: full`. The `otlp` exporters always get the normal record, so full content never leaves the machine through them. An integration with `content: full` and no file exporter is not refused: the setting is skipped with a warning, and no copy of the bodies is kept.

### Hashes

The record is the same as every exporter gets, with the same `hotline.record.hash`, so the file's copy rebuilds the signed batch too. The record also holds `hotline.content.hash`, over the content attributes but the bodies, as written, so with `REDACTED` where a secret was (Peter, 2026-11-18). The bodies are covered by `hotline.request.body.hash` and `hotline.response.body.hash`, which hash the whole body as it passed. `hotline-proxy verify` checks a body that was not cut against its hash; a cut one cannot be checked, and verify notes it. Detail: [proxy-ledger.md](proxy-ledger.md).

## Safety

- **One proxy per folder.** The proxy holds a lock on the folder (`.lock`, with the system's file lock, which is freed when the proxy ends, even when it dies). A second proxy on the same folder refuses to start and names the folder. Two file exporters on one folder refuse the config.
- **A folder that cannot be written refuses the config,** at start and at reload. The folder is made when missing, mode 0750, and the files are mode 0640.
- **Writing never slows a call.** The file exporter has its own queue, of at most 10 000 calls and `queueMemory` bytes. A full queue refuses the new call, which the usual warning line counts.
- **A failed write** (a full disk) loses that batch, counted in the same warning, and the next write tries again. If the folder or the current file is deleted or moved away while the proxy runs, the next write makes them again.
- **Changing a file exporter needs a restart,** as for every exporter ([proxy-config-reload.md](proxy-config-reload.md)); `content` and `maxBodySize` change on a reload.
- The proxy's own numbers for the `telemetry` section count each file exporter like any other, named by its folder.
