# The evidence ledger

*Agreed with Peter, 2026-11-13 to 2026-11-16, and built 2026-11-16. Scenarios: `src/hotline-proxy/features/ledger.feature` and `verify.feature`. The requirements, with the history of each choice: `evidence-ledger/requirements.md` in the Hotline project files. Replaces the signed records of 2026-10-30 (HMAC with `fingerprintKey`), which are gone.*

Every record, minimal or full, carries a hash of everything it holds. Every 10 seconds the proxy puts the hashes of each integration's records into a tree and signs the tree's top with a key it made itself. Each batch points to the one before, so a missing batch shows. Anyone can check the records with `hotline-proxy verify`, with no secret, no config and no network.

Some terms, in plain words:

- A **hash** is a short fingerprint of some bytes (here SHA-256). Anyone can compute it, and any change to the bytes changes it. A hash alone proves nothing, since whoever changes a record can compute a new one. The proof is that the hash rebuilds a root the proxy signed.
- A **tree** (a Merkle tree) pairs hashes up again and again until one hash is left, the **root**. Changing, adding or removing any record changes the root.
- A **signature** (here Ed25519) is made with a private key and checked with the matching public key. Checking gives no power to sign.
- A **chain**: each batch holds the root of the batch before, so a dropped, added or reordered batch breaks it.

## What a call record holds

| Attribute | What it holds |
|---|---|
| `hotline.record.hash` | `sha256:` and the hex of the record's hash. |
| `hotline.batch.id`, `hotline.batch.index` | The batch the record is in, and its place in that batch, from 0. |
| `hotline.trace.hash` | The hash of the call's trace (its spans). |
| `hotline.request.body.hash`, `hotline.response.body.hash` | The hash of each body as it passed, an empty body too. Only records with an HTTP exchange have them: a relayed tunnel or tcp connection, or a failed TLS handshake, has none. |
| `hotline.content.hash` | With `content: full` on a file exporter only: the hash of the content kept beside the record (below). |

### The record's hash

SHA-256 over the word `hotline.record.v1` as a piece, then every attribute but `hotline.record.hash` in name order, then the timestamp in microseconds since 1970 (a whole number) and the trace id and span id in hex, as if they were attributes named `log.record.timestamp`, `log.record.trace_id` and `log.record.span_id`.

Each part goes in as raw bytes, with no quoting and no escaping:

- A **piece** is its length as 8 bytes (big-endian), then its bytes.
- An **entry** is its name as a piece, then its value.
- A **value** is one letter for its type, then: `s` text as a piece; `i` whole number as 8 bytes; `f` decimal number as its 8 IEEE 754 bytes; `b` true or false as one byte; `l` list: the count as 8 bytes, then each item as a value; `m` map: the count as 8 bytes, then each entry in name order.

The lengths say where each piece ends, so no two different records give the same bytes.

### The trace's hash

SHA-256 over the word `hotline.trace.v1`, the trace id, then each span in start order (ties by span id). A span gives its id, parent id (empty for none), name, kind, start and end in microseconds, status (`unset`, `ok` or `error`), its count of attributes, then its attributes in name order.

### The body hashes

SHA-256 over the word `hotline.body.v1`, the word `request` or `response`, the trace id's 16 bytes, the span id's 8 bytes, then the body as it streamed. Both ids are known when the call starts, so the body is hashed as it passes, with no copy and no size limit. The ids are a public salt: they hide that two calls had the same body. They do not hide a short body from someone guessing it.

### Content kept beside the record

A file exporter with `content: full` writes the real values beside the record, under `hotline.content.*`: `url.full`, every request and response header, the bodies (base64) and whether each was cut short. Secrets are written as `REDACTED` ([proxy-file-export.md](proxy-file-export.md)), and the hash covers them as written, so `verify` rebuilds it from the file (Peter, 2026-11-18). The record keeps its normal, redacted attributes exactly as hashed. `hotline.content.hash` is SHA-256 over the word `hotline.content.v1` and the content attributes in name order, the bodies left out (the body hashes already cover them).

## What a batch record holds

A batch leaves as one OTLP log record, `event.name = hotline.ledger.batch`, through every exporter, beside the call records.

| Attribute | What it holds |
|---|---|
| `hotline.batch.format` | `hotline.batch.v1`. |
| `hotline.batch.id` | A UUID version 7, new for each batch. |
| `hotline.batch.integration` | The integration the chain is for, or `not-passed`. |
| `hotline.batch.instance` | The run's random `service.instance.id`, or the name `ledger.instance` sets. |
| `hotline.batch.run` | The random run id, only when `ledger.instance` sets the name. |
| `hotline.batch.sequence` | 0 for the first batch of the chain in this run, then 1, 2 and so on. |
| `hotline.batch.previous` | The root of the batch before. Not on sequence 0. |
| `hotline.batch.records`, `hotline.batch.root` | How many records, and the root of their tree. |
| `hotline.batch.key`, `hotline.batch.signature` | `ed25519:` and the base64 of the public key, and of the signature. |
| `hotline.batch.version`, `hotline.batch.config` | The proxy's version, and the hash of the config as loaded (`sha256:` and the hex). Secrets read from files or the environment are not part of it. |
| `hotline.batch.last` | `true` on the last batch of a clean stop. |
| `hotline.batch.dropped` | Per exporter: the index and hash of each record it dropped from the batch, up to 1,024. |
| `hotline.batch.dropped_count` | Per exporter, in place of the list, when it dropped more than 1,024. That copy cannot rebuild the root. |
| `hotline.batch.lost_batches` | Per exporter: how many batch records it lost before this one. |

The tree is the one from Certificate Transparency (RFC 6962): a leaf is SHA-256 of `0x00` and the record's hash, an inner node SHA-256 of `0x01`, left and right, and the split is the largest power of two below the count.

The signature is Ed25519 over the SHA-256 of the word `hotline.batch.v1` and every attribute in name order, but the signature, `dropped`, `dropped_count` and `lost_batches`, which differ per exporter. A wrong dropped hash does not rebuild the root, so the list needs no signature of its own.

## When batches close

- One chain per integration in the config, and one, `not-passed`, for everything else. A dispute is about one integration, so it hands over its own chain only.
- A batch closes every 10 seconds, or at 65,536 records. A window with no calls sends no batch.
- The proxy makes a new key in memory at start, and again every hour. The key is never written anywhere.
- Each run starts each chain at sequence 0. On a clean stop the proxy sends a last batch for every chain, an empty one too, with `hotline.batch.last`. A chain with no last batch means a crash or a cut-off end.
- A config reload changes `hotline.batch.config` from the next batch on. An integration added starts its chain at 0; one removed closes its chain with a last batch. A last batch sent empty names the config of the chain's batch before it, not the config in use at the time.
- Every record in a batch is of the config the batch names: the config its call started with, even when a reload came before the record was made. The first record of another config closes the open batch and starts a new one. A record of a call that started before its integration was removed goes on that integration's chain, after its last batch.
- A batch record is never refused for a full queue. At most 64 wait per exporter; when more pile up the oldest goes, and the next one sent says how many were lost.

```yaml
ledger:                       # optional; the values shown are the defaults
  batch:
    every: 10s
    maxRecords: 65536
  instance: { env: POD_NAME } # optional; left out, the name is random per run
```

## hotline-proxy verify

```
hotline-proxy verify <folder> [--integration id] [--body file --span id --side request|response] [--format json]
```

It reads a file exporter's folder (`calls.jsonl` and the rotated files, compressed or not) and checks each record's hash, each trace's hash from the spans kept in the line, the content hash and kept bodies, each batch's signature and root, and each run's chain. With `--body` it checks a body someone kept against the hash in the record with that span id.

It exits with 0 when everything checks out, 1 when it found a problem, and 2 when it could not read its input. With `--format json` it prints one object, `format: hotline.verify.v1`, with `ok`, `summary`, `runs`, `problems` and `notes`, each with a fixed `code` and a plain `message`.

- Problems: `record_hash_mismatch`, `trace_hash_mismatch`, `content_hash_mismatch`, `body_hash_mismatch`, `record_extra`, `record_missing`, `batch_signature_invalid`, `batch_root_mismatch`, `batch_unprovable`, `batches_lost`, `batch_missing`, `chain_broken`, `body_mismatch`, `body_record_missing`, `body_unreadable`, `input_unreadable`.
- Notes, which leave the exit code at 0: `records_dropped`, `records_unbatched`, `no_last_batch`, `body_not_checked`.

## Limits

- Without the control plane, the batches do not stop a deliberate forger: whoever holds the records can make a new key and sign new batches. The minute entry, the witness and the time stamp, which come with the control plane, anchor the key.
- A record an exporter loses after taking it, when a send or a write fails for good, is not in that exporter's dropped list, since the batch record has gone out already. That copy of the batch cannot rebuild its root.
- The build hash waits for secure hardware: a proxy can only report its own.
