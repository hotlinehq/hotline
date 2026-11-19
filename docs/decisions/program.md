# Program decisions (Peter, 2026-10-28)

Full list in [docs/proxy-program.md](../proxy-program.md).

1. Commands `run`, `validate` and `version`. The config comes only from `--config`, with no default.
2. Own logs: JSON, one object per line, on standard error, with `--log-level`.
3. Stopping: up to 25 seconds (`--shutdown-timeout`); a second signal cuts it short.
4. Exit codes: 0 clean, 1 any failure (also a stop that ran out of time), 2 wrong command line.
5. `--trust-file` adds trusted certificate authorities on top of the system's own, so the end-to-end tests can trust their test vendor on any computer. This changed the spec's vendor certificate rule.
6. End-to-end tests use only the Go client for now. They run with `make e2e`, and `make test` adds their coverage to the 100% check.
7. The loaded config offers one `Integration` per ID, with the not-passed entry's `urlFull` and `content` filled in where the integration says nothing, wherever the entry is in the file. A call looks its integration up once (`Config.Integration`) and keeps it; the separate lookups for apis, trace, urlFull, content and the ledger chain are gone. The YAML is unchanged (Peter, 2026-11-16, from the decomplect review).
