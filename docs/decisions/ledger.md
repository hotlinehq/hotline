# Evidence ledger decisions (Peter, 2026-11-13 to 2026-11-16)

Full wording in [docs/proxy-ledger.md](../proxy-ledger.md). Requirements, with the history of each choice: `evidence-ledger/requirements.md` in the project files. Scenarios: `features/ledger.feature` and `features/verify.feature`.

1. Every record, minimal or full, carries a SHA-256 hash, `hotline.record.hash`, not an HMAC. `fingerprintKey`, the HMAC attributes and the old signature format are gone, with no special handling of an old config (still the edge version).
2. Only batches are signed, with Ed25519 and a key the proxy makes in memory at start and every hour. A batch is an RFC 6962 tree of record hashes.
3. One chain per integration in the config, plus `not-passed`. Each run starts each chain at sequence 0; a clean stop sends a last batch for each, an empty one too.
4. A batch closes every 10 seconds or at 65,536 records (`ledger.batch.every`, `maxRecords`). A window with no calls sends no batch.
5. A batch leaves as one OTLP log record, `hotline.ledger.batch`, through every exporter. Each exporter adds the hashes of the records it dropped (up to 1,024, then only a count). At most 64 batch records wait per exporter; past that the oldest goes and the next one says how many were lost. *Changed by 19 and 20 (2026-11-25): the dropped hashes are in a signed drop record of their own, and nothing says how many were lost.*
6. The batch carries the proxy's version and the config's hash (secrets left out), signed.
7. `ledger.instance` can name the instance (for example `{ env: POD_NAME }`); the batch then also carries the random run id.
8. Body hashes are salted with the call's trace id and span id, no secret. Every record with an HTTP exchange has both, an empty body hashed as zero bytes.
9. `content: full` keeps the real values beside the record under `hotline.content.*`, covered by `hotline.content.hash` (bodies left out: the body hashes cover them).
10. `hotline-proxy verify <folder>` checks a file exporter's folder with no config, key or network. Exit 0, 1 or 2; `--format json` gives `hotline.verify.v1`.
11. Every record in a batch is of the config the batch names: the config its call started with. A record of another config closes the open batch first. A call still in flight when its integration is removed goes on that integration's chain, after the last batch (Peter, 2026-11-16, from the decomplect review). The same holds for short records: a relay, a refused request or a failed tunnel belongs to the config in use when it arrived, however long a relay lasts (Peter, 2026-11-16).
12. Each exporter keeps the records of a batch it dropped and adds them to that batch's record itself, so the ledger never matches exporters by their place in a list (Peter, 2026-11-16, over keying by endpoint, which two exporters may share). *Still each exporter's own since 19 (2026-11-25), in its drop record rather than the batch record.*
13. An empty last batch, sent when a reload removes an integration or at a clean stop, names the config of the chain's batch before it, not the config in use then (Peter, 2026-11-17, from the decomplect review).
14. Later, with the control plane: the minute entry, the witness, the qualified time stamp, and secure hardware for the build hash.

## Every field covered (Peter, 2026-11-25, built 2026-11-25)

From the ADR check: the record hash left out some fields the proxy sends, such as the level, so a failed call's ERROR could become INFO unseen. Requirements, reviewed with decomplect: `decision-tripwires/record-hash-v2-requirements.md` in the project files. Breaking changes are allowed, since only edge builds exist.

15. The record hash covers every field the proxy writes, a closed list by OTLP field number: the attributes, time, trace id, span id, trace flags, level, event name, body, observed time and the full resource as sent (Q5). `verify` reads only the new format.
16. The observed time is read once from the proxy's clock before the hash, and that value is both hashed and sent. It still says when the record was written, such as a WebSocket session's end (Q6).
17. The resource labels are no longer per exporter. A top-level `records.transform` list holds steps run on every record before the hash; the only kind for now is `labels`. A `resource` under an exporter refuses the config (Q7, Q8).
18. The trace hash covers span links and span flags, and every time is cut to whole microseconds before it is hashed and sent.
19. The batch record is the same for every exporter and signed once by the ledger. An exporter that dropped records of a batch sends a separate drop record, `hotline.ledger.dropped`, signed when the batch is handed over, with the key passed beside it (Q9, and the decomplect review).
20. `hotline.batch.lost_batches` is removed: the signed chain shows a missing batch, and the proxy's own telemetry says which exporter lost it (Q10).

## Tripwires (2026-11-25)

- Confirmation: `hotline-proxy verify` passes on the folder a file exporter wrote during a short run of the proxy, and every field of a record is covered by its hash.
- Revisit if batches of 10 seconds are too slow for the control plane, a run often fills a batch at 65,536 records, or a customer needs a signing key that outlives one run.
