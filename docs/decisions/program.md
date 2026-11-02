# Program decisions (Peter, 2026-10-28)

Full list in [docs/proxy-program.md](../proxy-program.md).

1. Commands `run`, `validate` and `version`. The config comes only from `--config`, with no default.
2. Own logs: JSON, one object per line, on standard error, with `--log-level`.
3. Stopping: up to 25 seconds (`--shutdown-timeout`); a second signal cuts it short.
4. Exit codes: 0 clean, 1 any failure (also a stop that ran out of time), 2 wrong command line.
5. `--trust-file` adds trusted issuers on top of the system's own, so the end-to-end tests can trust their test vendor on any computer. This changed the spec's vendor certificate rule.
6. End-to-end tests use only the Go client for now. They run with `make e2e`, and `make test` adds their coverage to the 100% check.
