# Evidence ledger decisions (Peter, 2026-11-13 to 2026-11-16)

Full wording in [docs/proxy-ledger.md](../proxy-ledger.md). Requirements, with the history of each choice: `evidence-ledger/requirements.md` in the project files. Scenarios: `features/ledger.feature` and `features/verify.feature`.

1. Every record, minimal or full, carries a SHA-256 hash, `hotline.record.hash`, not an HMAC. `fingerprintKey`, the HMAC attributes and the old signature format are gone, with no special handling of an old config (still the edge version).
2. Only batches are signed, with Ed25519 and a key the proxy makes in memory at start and every hour. A batch is an RFC 6962 tree of record hashes.
3. One chain per integration in the config, plus `not-passed`. Each run starts each chain at sequence 0; a clean stop sends a last batch for each, an empty one too.
4. A batch closes every 10 seconds or at 65,536 records (`ledger.batch.every`, `maxRecords`). A window with no calls sends no batch.
5. A batch leaves as one OTLP log record, `hotline.ledger.batch`, through every exporter. Each exporter adds the hashes of the records it dropped (up to 1,024, then only a count). At most 64 batch records wait per exporter; past that the oldest goes and the next one says how many were lost.
6. The batch carries the proxy's version and the config's hash (secrets left out), signed.
7. `ledger.instance` can name the instance (for example `{ env: POD_NAME }`); the batch then also carries the random run id.
8. Body hashes are salted with the call's trace id and span id, no secret. Every record with an HTTP exchange has both, an empty body hashed as zero bytes.
9. `content: full` keeps the real values beside the record under `hotline.content.*`, covered by `hotline.content.hash` (bodies left out: the body hashes cover them).
10. `hotline-proxy verify <folder>` checks a file exporter's folder with no config, key or network. Exit 0, 1 or 2; `--format json` gives `hotline.verify.v1`.
11. Every record in a batch is of the config the batch names: the config its call started with. A record of another config closes the open batch first. A call still in flight when its integration is removed goes on that integration's chain, after the last batch (Peter, 2026-11-16, from the decomplect review). The same holds for short records: a relay, a refused request or a failed tunnel belongs to the config in use when it arrived, however long a relay lasts (Peter, 2026-11-16).
12. Each exporter keeps the records of a batch it dropped and adds them to that batch's record itself, so the ledger never matches exporters by their place in a list (Peter, 2026-11-16, over keying by endpoint, which two exporters may share).
13. Later, with the control plane: the minute entry, the witness, the qualified time stamp, and secure hardware for the build hash.
