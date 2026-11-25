# File exporter decisions (Peter, 2026-11-11)

Full wording in [docs/proxy-file-export.md](../proxy-file-export.md). Requirements: `file-exporter/requirements.md` in the project files.

1. A `file` exporter writes each call as one line of flat JSON to `calls.jsonl`, with OpenTelemetry's names, not OTLP's own JSON envelope. Most uses are local.
2. A call's spans are inside its line, as `hotline.trace.spans`. No traces file, no metrics in the file, no setting for either.
3. Lines are written at most 100 ms after the record is made, fixed in the program; `logInterval` does not apply.
4. Rotation by size only; rotated files are gzipped by default, the current file never.
5. `content: full` is an integration setting (and the not-passed entry's), not an exporter's. It goes to file exporters only, and is skipped with a warning when there is none.
6. Under full content: every header as sent, `Proxy-Authorization` kept but `REDACTED`, other proxy headers as sent; bodies as base64, as they passed (a gzipped body stays gzipped), cut at `maxBodySize` (default 1 MiB) with a `truncated` flag; `url.full` as sent.
7. Signatures: body signatures stay over the whole body, as today; the file's record is signed on its own, with every header. Minimal records stay unsigned.
8. Paths count from the config's folder; one proxy per folder, held with a lock; the queue is capped by memory (`queueMemory`, default 256 MiB).
9. The lines are written with Go's `encoding/json/jsontext`, with no new dependency. No `path: stdout` and no rotation by time, for now.

Changed 2026-11-16 with the evidence ledger ([docs/decisions/ledger.md](ledger.md)): full content goes beside the record under `hotline.content.*`, with `hotline.content.hash`, and the record stays as every exporter gets it. This replaces 6's names and 7.

Changed 2026-11-18 (Peter): under `content: full`, secrets are written as `REDACTED`, so no vendor key is copied out of a file kept for debugging. Secret, always: `Authorization`, `Proxy-Authorization`, `Cookie` and `Set-Cookie`; with a spec, also the login its security section names and every value marked `x-hotline-redact: true`. Other values stay as sent. `hotline.content.hash` covers the content as written, with `REDACTED` in it, so `hotline-proxy verify` works unchanged. Revisit if apps often use `content: full` for vendors that have no spec (then a fixed list of key headers), or if someone must later prove which key an app used (then a keyed hash of the secret instead of `REDACTED`). The rule lives in `spec.Hide`, beside the record's `Redact`.

## Tripwires (2026-11-25)

- Confirmation: the file exporter writes its lines with Go's `encoding/json/jsontext` (`jsontext.NewEncoder` in `internal/export/line.go`).
- Revisit if: see the Revisit if lines in the decisions above.
