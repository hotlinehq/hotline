# The evidence ledger

*Agreed with Peter, 2026-11-13 to 2026-11-16, and built 2026-11-16. Every field covered: decided and built 2026-11-25 ([decisions 15 to 20](decisions/ledger.md)). Each exporter signs its own batch record, with a self-signed certificate for the key: decided and built 2026-11-26 (decisions 21 to 25). Signing sources, memory and a file authority: decided and built 2026-11-26 (decisions 26 to 31). Scenarios: `src/hotline-proxy/features/ledger.feature` and `verify.feature`. The requirements, with the history of each choice: `evidence-ledger/requirements.md` in the Hotline project files. Replaces the signed records of 2026-10-30 (HMAC with `fingerprintKey`), which are gone.*

Every record, minimal or full, carries a hash of everything it holds. Every 10 seconds the proxy puts the hashes of each integration's records into a tree, and each exporter signs the tree's top, with a key the proxy made itself, in its own batch record. Each batch points to the one before, so a missing batch shows. Anyone can check the records with `hotline-proxy verify`, with no secret, no config and no network.

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

The hash covers every field the proxy sends on the record, not only its attributes (Peter, 2026-11-25). SHA-256 over the word `hotline.record.v2` as a piece, then every attribute but `hotline.record.hash` in name order, then these fields, in this order, as if they were attributes with these names:

| Name | What it holds |
|---|---|
| `log.record.timestamp` | The record's time, in microseconds since 1970 (a whole number). |
| `log.record.observed_time` | When the record was written, the same way. For a WebSocket session or a stream, that is its end. |
| `log.record.trace_id`, `log.record.span_id` | The trace id and span id in hex. |
| `log.record.flags` | The trace's flags, a whole number (1 when the app sampled the trace). |
| `log.record.severity_number`, `log.record.severity_text` | The level, as OTLP numbers it (9 for `INFO`, 13 for `WARN`, 17 for `ERROR`) and its name. |
| `log.record.event_name` | The event, such as `hotline.request`. |
| `log.record.body` | The record's text: a probe's sentence, empty for every other record. |
| `log.record.resource` | The resource the record is sent with, as one map: the same for every exporter. |

That is the closed list of what the proxy sends on a record: the OTLP LogRecord fields 1, 2, 3, 5, 6, 8, 9, 10, 11 and 12, and the resource. The scope name is always `hotline-proxy`. A test fails when the proxy sends a field that is not on the list. Every time is cut to whole microseconds before it is hashed and sent, so the hash covers the time as sent. The observed time is read once, before the hash, so the time sent is the time hashed.

Each part goes in as raw bytes, with no quoting and no escaping:

- A **piece** is its length as 8 bytes (big-endian), then its bytes.
- An **entry** is its name as a piece, then its value.
- A **value** is one letter for its type, then: `s` text as a piece; `i` whole number as 8 bytes; `f` decimal number as its 8 IEEE 754 bytes; `b` true or false as one byte; `l` list: the count as 8 bytes, then each item as a value; `m` map: the count as 8 bytes, then each entry in name order.

The lengths say where each piece ends, so no two different records give the same bytes.

### The trace's hash

SHA-256 over the word `hotline.trace.v2`, the trace id, then each span in start order (ties by span id). A span gives its id, parent id (empty for none), name, kind, start and end in microseconds, status (`unset`, `ok` or `error`), its flags as OTLP sends them (the trace's flags, and whether its parent is in another program), its count of links, then each link's trace id, span id and flags, then its count of attributes, then its attributes in name order. A link is a span of another trace that this one follows from, such as the failed call that made a probe due.

### The body hashes

SHA-256 over the word `hotline.body.v1`, the word `request` or `response`, the trace id's 16 bytes, the span id's 8 bytes, then the body as it streamed. A WebSocket session's bytes are hashed the same way, with the side `from_app` or `from_vendor` and the session span's id (Peter, 2026-11-18). Both ids are known when the call starts, so the body is hashed as it passes, with no copy and no size limit. The ids are a public salt: they hide that two calls had the same body. They do not hide a short body from someone guessing it.

### Content kept beside the record

A file exporter with `content: full` writes the real values beside the record, under `hotline.content.*`: `url.full`, every request and response header, the bodies (base64) and whether each was cut short. Secrets are written as `REDACTED` ([proxy-file-export.md](proxy-file-export.md)), and the hash covers them as written, so `verify` rebuilds it from the file (Peter, 2026-11-18). The record keeps its normal, redacted attributes exactly as hashed. `hotline.content.hash` is SHA-256 over the word `hotline.content.v1` and the content attributes in name order, the bodies left out (the body hashes already cover them).

## What a batch record holds

A batch leaves as one OTLP log record per exporter, `event.name = hotline.ledger.batch`, after the batch's call records. The ledger builds the batch and signs nothing. It asks the vault (the proxy's in-memory place for the signing key) for a signer once, and hands the batch and the signer to every exporter. Each exporter adds its own name and the records of the batch it dropped, and signs its own batch record (Peter, 2026-11-26). It fixes the record under the ledger's lock and signs it afterwards, on its own, so no call waits on signing. So each copy holds one batch record per batch, and a copy mixed from two exporters shows. Batch records of the same batch differ between exporters only in `hotline.exporter`, the dropped records, the level and the signature.

| Attribute | What it holds |
|---|---|
| `hotline.batch.format` | `hotline.batch.v3`. |
| `hotline.batch.id` | A UUID version 7, new for each batch. |
| `hotline.batch.integration` | The integration the chain is for, or `not-passed`. |
| `hotline.batch.instance` | The run's `service.instance.id`: the one the `records.transform` labels set, or a random id per run. |
| `hotline.batch.run_started` | When this run of the proxy started, in UTC, to the microsecond, so two runs of the same instance are told apart. On every batch. |
| `hotline.batch.sequence` | 0 for the first batch of the chain in this run, then 1, 2 and so on. |
| `hotline.batch.previous` | The root of the batch before. Not on sequence 0. |
| `hotline.batch.records`, `hotline.batch.root` | How many records, and the root of their tree. |
| `hotline.batch.version`, `hotline.batch.config` | The proxy's version, and the hash of the config as loaded (`sha256:` and the hex). Secrets read from files or the environment are not part of it. |
| `hotline.batch.last` | `true` on the last batch of a clean stop. |
| `hotline.exporter` | The exporter that sent this batch record: its endpoint, or a file exporter's path. |
| `hotline.batch.dropped_records` | The index and hash of each record of the batch this exporter dropped because its queue was full, up to 1,024. An empty list when it dropped none. |
| `hotline.batch.dropped_count` | In place of the list, when it dropped more than 1,024. That copy cannot rebuild the root. |
| `hotline.batch.certificate` | The certificate of the key that signed the batch record: base64 of its DER bytes (the standard binary form of a certificate). |
| `hotline.batch.authorities` | Only under an authority: the certificates above the batch certificate, the one that signed it first, each as base64 of its DER bytes. |
| `hotline.batch.signature` | The algorithm, a colon, and the base64 of the signature: `ed25519:`, `ml-dsa-44:`, `ml-dsa-65:` or `ml-dsa-87:` from the proxy; `verify` also reads `ecdsa-sha256:`, `rsa-pss-sha256:` and `rsa-pkcs1-sha256:`. |

The level is `INFO`, or `WARN` when the exporter dropped records of the batch.

The certificate is an X.509 certificate for the hourly key (Ed25519 unless `ledger.signing.hourlyKeyType` says an ML-DSA type), made by the vault and marked "not an authority". Without `ledger.signing.cert` it is signed by that key itself (Peter, 2026-11-26); with it, by the authority from `certs` (a key whose job is to vouch for other keys). Its name (common name) is the service, `service.name` from the resource, not the pod; the batch record names its pod as the instance. It is valid from when the key was made for one hour, the start cut down to a whole second and the end cut down plus one second. A self-signed certificate vouches for nothing outside the folder; it gives the key a name and an hour. One under an authority leads to a root authority the person checking can choose to trust.

The tree is the one from Certificate Transparency (RFC 6962): a leaf is SHA-256 of `0x00` and the record's hash, an inner node SHA-256 of `0x01`, left and right, and the split is the largest power of two below the count.

The signature is over the SHA-256 of the word `hotline.batch.v3`, every attribute but the signature in name order, then the record's other fields as a record's hash covers them: its time, observed time, level, event name and resource (with an empty trace, flags and body). Ed25519 and ML-DSA sign that 32-byte digest as it is; ECDSA and RSA sign it as an already hashed SHA-256 value. An exporter fixes the batch record when it queues it, once its drops for the batch are final, signs it right after, and keeps no key: a retry sends the same bytes. A batch record that cannot be signed is let go with a warning, and the chain shows it missing.

`verify` takes the dropped records' hashes in place of the records, so a copy with drops still rebuilds the root. A record deleted and listed as dropped fails the signature. A folder is one exporter's copy, so `verify` works out the exporter most of its batch records name, says whose copy the folder is, and refuses a batch record that names another exporter or lists a record the folder still holds: each is a batch record taken from another copy.

## When batches close

- One chain per integration in the config, and one, `not-passed`, for everything else. A dispute is about one integration, so it hands over its own chain only.
- A batch closes every 10 seconds, or at 65,536 records. A window with no calls sends no batch.
- The vault makes a new key in memory at start, again every hour, and at once when a reload changes the key type or the authority, so no batch is signed under an authority after it was replaced; the ledger asks it once for each batch it closes, so every exporter signs a batch with the same key. Exporters never ask the vault themselves, since a new key made between two of them would split a batch across two keys. The key is never written anywhere.
- Each run starts each chain at sequence 0. On a clean stop the proxy sends a last batch for every chain, an empty one too, with `hotline.batch.last`. A chain with no last batch means a crash or a cut-off end.
- A config reload changes `hotline.batch.config` from the next batch on. An integration added starts its chain at 0; one removed closes its chain with a last batch. A last batch sent empty names the config of the chain's batch before it, not the config in use at the time.
- Every record in a batch is of the config the batch names: the config its call started with, even when a reload came before the record was made. The first record of another config closes the open batch and starts a new one. A record of a call that started before its integration was removed goes on that integration's chain, after its last batch.
- A batch record is never refused for a full queue. At most 64 wait per exporter; when more pile up the oldest goes. The chain shows it as missing, and the proxy's own `hotline.exporter.dropped`, with `hotline.signal batch`, says which exporter lost it (Peter, 2026-11-25).

```yaml
ledger:                       # optional; the values shown are the defaults
  batch:
    every: 10s
    maxRecords: 65536
  signing:                     # optional; left out, Ed25519 keys that sign their own certificates
    hourlyKeyType: ed25519     # or ml-dsa-44, ml-dsa-65, ml-dsa-87
    cert: batch-authority      # optional; a name in certs, whose authority signs the hourly keys
```

`ledger.signing.cert` names a certificate and key pair in `certs`, checked as every cert is (the pair matches and has not expired, the key file is kept out of the config hash), and also that it is a certificate authority and that each certificate in its file is signed by the next one. Its file holds the authority first, then the ones above it; its key may be Ed25519, ECDSA, RSA or ML-DSA. A refusal at start stops the proxy; at a reload the old config stays in use. An authority that expires while the proxy runs still signs: the proxy logs a warning on each hourly key made under it, and its expiry shows in `hotline.certificate.not_after` with use `ledger.signing`; `verify` refuses those batches.

## hotline-proxy verify

```
hotline-proxy verify <folder> [--integration id] [--body file --span id --side request|response] [--root-authority file]... [--format json]
```

It reads a file exporter's folder (`calls.jsonl` and the rotated files, compressed or not) and checks each record's hash, each trace's hash from the spans kept in the line, the content hash and kept bodies, each batch's signature and root, and each run's chain. With `--body` it checks a body someone kept against the hash in the record with that span id. A batch certificate signed by its own key passes as self-signed. Any other must link up, through the batch's authorities, to a root authority it trusts: the computer's public ones, or one in a PEM file given with `--root-authority`, which may repeat. Each run and the summary say `batch_signing` (`self_signed`, `root_authority`, or `both` when a reload brought an authority in the middle of a run) and `root_authorities`, the names of the roots its batches lead to.

It reads only the formats of this build: a folder written by an older edge build, with `hotline.batch.v1` or `hotline.batch.v2`, is refused as unreadable, with a message that says so.

It exits with 0 when everything checks out, 1 when it found a problem, and 2 when it could not read its input. With `--format json` it prints one object, `format: hotline.verify.v1`, with `ok`, `summary` (with `exporter`, whose copy the folder is), `runs`, `problems` and `notes`, each with a fixed `code` and a plain `message`.

- Problems: `batch_certificate_invalid` (a certificate that cannot be read, is not signed by its own key with no authority named, claims to be an authority, or names another service than the batch record), `batch_authority_unlinked` (a certificate not signed by the authority after it, or an authority that is not one), `batch_authority_untrusted` (authorities that lead to no root it trusts), `batch_outside_authority` (a batch closed outside the time an authority is valid), `batch_outside_certificate` (a batch closed outside its certificate's hour), `record_hash_mismatch`, `trace_hash_mismatch`, `content_hash_mismatch`, `body_hash_mismatch`, `record_extra`, `record_missing`, `batch_signature_invalid`, `batch_root_mismatch`, `batch_unprovable`, `batch_record_foreign`, `batch_missing`, `chain_broken`, `body_mismatch`, `body_record_missing`, `body_unreadable`, `input_unreadable`.
- Notes, which leave the exit code at 0: `key_self_certified` (on a run with self-signed batches: nothing outside the folder vouches for the key), `records_dropped`, `records_unbatched`, `no_last_batch`, `body_not_checked`, `records_repeated` and `batch_record_repeated` (a record or a batch record sent twice, the same in every field, as a send to a collector that was tried again can leave: it is counted once, and one that differs in anything stays `record_extra` or `chain_broken`; Peter, 2026-11-27).

## Limits

- Without the control plane, the batches do not stop a deliberate forger: whoever holds the records can make a new key and sign new batches. The minute entry, the witness and the time stamp, which come with the control plane, anchor the key.
- A record an exporter loses after taking it, when a send or a write fails for good, is not in that exporter's dropped list, since the batch record has gone out already. That copy of the batch cannot rebuild its root.
- Without an authority, the key's certificate is signed by the key itself, so nothing outside the folder vouches for it, and a folder that names one exporter throughout is only as strong as the key. With a file authority, whoever can read the authority's key file can sign keys for any hour, past ones too; an outside service such as OpenBao, which is not built yet, keeps the authority's key away from the proxy.
- The build hash waits for secure hardware: a proxy can only report its own.
