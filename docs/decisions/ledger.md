# Evidence ledger decisions (Peter, 2026-11-13 to 2026-11-16)

Full wording in [docs/proxy-ledger.md](../proxy-ledger.md). Requirements, with the history of each choice: `evidence-ledger/requirements.md` in the project files. Scenarios: `features/ledger.feature` and `features/verify.feature`.

1. Every record, minimal or full, carries a SHA-256 hash, `hotline.record.hash`, not an HMAC. `fingerprintKey`, the HMAC attributes and the old signature format are gone, with no special handling of an old config (still the edge version).
2. Only batches are signed, with Ed25519 and a key the proxy makes in memory at start and every hour. A batch is an RFC 6962 tree of record hashes.
3. One chain per integration in the config, plus `not-passed`. Each run starts each chain at sequence 0; a clean stop sends a last batch for each, an empty one too.
4. A batch closes every 10 seconds or at 65,536 records (`ledger.batch.every`, `maxRecords`). A window with no calls sends no batch.
5. A batch leaves as one OTLP log record, `hotline.ledger.batch`, through every exporter. Each exporter adds the hashes of the records it dropped (up to 1,024, then only a count). At most 64 batch records wait per exporter; past that the oldest goes and the next one says how many were lost. *Changed by 19 and 20 (2026-11-25): the dropped hashes are in a signed drop record of their own, and nothing says how many were lost. Changed by 22 (2026-11-26): in the exporter's own batch record.*
6. The batch carries the proxy's version and the config's hash (secrets left out), signed.
7. `ledger.instance` can name the instance (for example `{ env: POD_NAME }`); the batch then also carries the random run id. *Changed by 32 (2026-11-27): the instance is the `service.instance.id` label.*
8. Body hashes are salted with the call's trace id and span id, no secret. Every record with an HTTP exchange has both, an empty body hashed as zero bytes.
9. `content: full` keeps the real values beside the record under `hotline.content.*`, covered by `hotline.content.hash` (bodies left out: the body hashes cover them).
10. `hotline-proxy verify <folder>` checks a file exporter's folder with no config, key or network. Exit 0, 1 or 2; `--format json` gives `hotline.verify.v1`.
11. Every record in a batch is of the config the batch names: the config its call started with. A record of another config closes the open batch first. A call still in flight when its integration is removed goes on that integration's chain, after the last batch (Peter, 2026-11-16, from the decomplect review). The same holds for short records: a relay, a refused request or a failed tunnel belongs to the config in use when it arrived, however long a relay lasts (Peter, 2026-11-16).
12. Each exporter keeps the records of a batch it dropped and adds them to that batch's record itself, so the ledger never matches exporters by their place in a list (Peter, 2026-11-16, over keying by endpoint, which two exporters may share). *Still each exporter's own since 19 (2026-11-25), in its drop record rather than the batch record. Changed by 22 (2026-11-26): in the exporter's own batch record.*
13. An empty last batch, sent when a reload removes an integration or at a clean stop, names the config of the chain's batch before it, not the config in use then (Peter, 2026-11-17, from the decomplect review).
14. Later, with the control plane: the minute entry, the witness, the qualified time stamp, and secure hardware for the build hash.

## Every field covered (Peter, 2026-11-25, built 2026-11-25)

From the ADR check: the record hash left out some fields the proxy sends, such as the level, so a failed call's ERROR could become INFO unseen. Requirements, reviewed with decomplect: `decision-tripwires/record-hash-v2-requirements.md` in the project files. Breaking changes are allowed, since only edge builds exist.

15. The record hash covers every field the proxy writes, a closed list by OTLP field number: the attributes, time, trace id, span id, trace flags, level, event name, body, observed time and the full resource as sent (Q5). `verify` reads only the new format.
16. The observed time is read once from the proxy's clock before the hash, and that value is both hashed and sent. It still says when the record was written, such as a WebSocket session's end (Q6).
17. The resource labels are no longer per exporter. A top-level `records.transform` list holds steps run on every record before the hash; the only kind for now is `labels`. A `resource` under an exporter refuses the config (Q7, Q8).
18. The trace hash covers span links and span flags, and every time is cut to whole microseconds before it is hashed and sent.
19. The batch record is the same for every exporter and signed once by the ledger. An exporter that dropped records of a batch sends a separate drop record, `hotline.ledger.dropped`, signed when the batch is handed over, with the key passed beside it (Q9, and the decomplect review). *Replaced by 22 (2026-11-26): each exporter signs its own batch record, and there is no drop record.*
20. `hotline.batch.lost_batches` is removed: the signed chain shows a missing batch, and the proxy's own telemetry says which exporter lost it (Q10).

## Each exporter signs its own batch record (Peter, 2026-11-26)

`verify` could not tell that a drop record came from the folder's own exporter: an exporter that dropped nothing sent none, so a record deleted from one copy and covered by another exporter's real drop record passed as a drop. Requirements: `evidence-ledger/verify-own-exporter-requirements.md` in the project files.

21. Every exporter sends its own signed record for every batch, also when it dropped nothing, so `verify` can tell whose copy a folder is (Q1, over a flag naming the exporter).
22. One record per batch per exporter: the ledger builds the batch and signs nothing; each exporter adds `hotline.exporter` and `hotline.batch.dropped_records` (empty when none) or `hotline.batch.dropped_count`, and signs the whole batch record, `hotline.batch.v3`, at `WARN` when it dropped records. The drop record and `hotline.ledger.dropped` are gone (Q2). `verify` takes the exporter most batch records name as the copy's, refuses others as `batch_record_foreign`, and says whose copy it is (`summary.exporter`).
23. The vault, the proxy's in-memory place for the key, makes a new key at start and every hour. The ledger asks it once per batch and hands the key to every exporter beside the batch; exporters never ask it, so a batch is never split across two keys (Q3). It lives in its own package, `internal/vault`, which knows nothing of the ledger; how a signature is written into a batch record stays in the ledger (Peter, 2026-11-26).

24. Each hourly key comes with an X.509 certificate the vault makes and the key signs itself, valid for the key's hour, carried as `hotline.batch.certificate` in place of `hotline.batch.key` (Q4: in the same pull request, since `hotline.batch.v3` was not released). A certificate from a trusted source is the next step. *Done by 28 (2026-11-26): a file authority can sign it.*
25. The certificate names the service (`service.name`), not the pod (Q5). `verify` refuses one that cannot be read, is not signed by its own key or names another service (`batch_certificate_invalid`), and a batch closed outside its hour (`batch_outside_certificate`); every run gets the note `key_self_certified`. *Changed by 28 and 31 (2026-11-26): a certificate signed by an authority passes when it leads to a trusted root, one claiming to be an authority is refused, and only runs with self-signed batches get the note.*

- Revisit if the trusted source to come issues a certificate per pod rather than per service (Q5).
- Revisit if a remote vault such as OpenBao is put behind the vault (the ledger already asks for a signer rather than a key, since 27), and the `certs` keys move under the vault too, as OpenBao would host them (Peter, 2026-11-26: a next step). A certificate for each hourly key from a trusted source is its own later topic.

## Signing sources: memory and a file authority (Peter, 2026-11-26)

The batch certificate was self-signed, so nothing outside the folder vouched for the key. Spike and requirements: `batch-signing/spike.md` and `batch-signing/requirements.md` in the project files.

26. The source of the signing key is a module chosen in the config (Q1). Built in this order: memory, then a file authority, then an outside service that issues key and certificate (OpenBao PKI), then one that only signs (OpenBao transit) (Q3). Memory and the file authority are built; the OpenBao sources are not.
27. The vault gives the ledger a signer once per batch, not a key: it signs all of a batch's digests in one go, can fail, and gives the certificate and the authorities to put beside the signatures. Each exporter fixes its batch record under the lock and signs afterwards, on its own, so no call waits on signing and a remote signer later needs one call per batch. A batch record that cannot be signed is let go with a warning, and the chain shows it missing.
28. With `ledger.signing.cert`, the certificate authority the config names in `certs` signs each hourly key's certificate; the proxy still makes the keys in memory (Q4). Any authorities above it are accepted, with no path length limit. The hourly certificate is marked "not an authority". A new authority at a reload makes a new hourly key at once; a refused one keeps the old.
29. An authority that expires while the proxy runs still signs; the proxy warns on each hourly key made under it and reports its expiry in `hotline.certificate.not_after` with use `ledger.signing`, and `verify` refuses those batches (`batch_outside_authority`) (Q7).
30. The proxy's own keys are Ed25519 by default; `ledger.signing.hourlyKeyType` can choose ML-DSA-44, ML-DSA-65 or ML-DSA-87 (Q8). `verify` checks Ed25519, ECDSA, RSA and ML-DSA, named in front of each signature (`ed25519:`, `ecdsa-sha256:`, `rsa-pss-sha256:`, `rsa-pkcs1-sha256:`, `ml-dsa-44:` and so on) (Q6).
31. `verify` trusts the computer's public root authorities and the ones given with `--root-authority` (Q5). It reports no levels: each run and the summary say `batch_signing`, `self_signed`, `root_authority` or `both`, with the `root_authorities` its batches lead to (Q10, which replaced Q2 and dropped the policy numbers of Q9). The target is a dispute with a vendor; the qualified time-stamp it needs is a control plane topic.

- Revisit if users can only get plain certificates (for example from cert-manager), not an authority certificate (Q4).
- Revisit if ML-DSA certificates turn out not to verify with the standard library (Q6).
- Revisit if a customer needs post-quantum evidence (Q8).
- Revisit if expired batches go unnoticed until verify runs (Q7).

## The instance is service.instance.id (Peter, 2026-11-27)

32. `ledger.instance` is gone: it named the same thing as the `service.instance.id` label in `records.transform`, and the two could disagree. A batch's `hotline.batch.instance` is the resource's `service.instance.id`, the label or a random id per run; when the label sets it, the batch also carries the random run id, `hotline.batch.run`. An old `ledger.instance` gets the usual warning for a setting the proxy does not know.

## Tripwires (2026-11-25)

- Confirmation: `hotline-proxy verify` passes on the folder a file exporter wrote during a short run of the proxy, and every field of a record is covered by its hash.
- Revisit if batches of 10 seconds are too slow for the control plane, a run often fills a batch at 65,536 records, or a customer needs a signing key that outlives one run.
