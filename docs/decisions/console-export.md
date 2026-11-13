# Console exporter decisions (Peter, 2026-11-11)

Full wording in [docs/proxy-console-export.md](../proxy-console-export.md). Requirements: `console-exporter/requirements.md` in the project files.

1. A `console` exporter prints each call to standard output, modelled on mitmdump: two lines per call (request, then reply under it), and a third with what the checks found for an invalid call.
2. Basic information only: time, integration, method, address, status, failure, reply size, total time, check status. No headers, bodies, spans or metrics.
3. Colours close to mitmdump's; colour only on a terminal and without `NO_COLOR`, unless `color: always` or `never`. `color` is the only setting.
4. Long addresses are cut as mitmdump does: width less 25, at least 50; width from the terminal, then `COLUMNS`, then 80. A terminal width of 0 counts as unknown, and the terminal is asked once per batch of lines, not per call (Peter, 2026-11-13).
5. Every value from a call is escaped before printing. The proxy's logs stay on standard error.
6. One console exporter at most; lines wait in a bounded queue and are printed at once.
7. Every command takes `--format console|json` (Peter, 2026-11-12). `console`, the default, writes the program's own lines as plain coloured lines on standard error. `json` makes everything JSON: the logs, `version`, and the console exporter's calls (the file exporter's line, without `content: full`). The Docker image and the Kubernetes sidecar add `--format json`.
8. The console never shows more of a call's address than its record holds (Peter, 2026-11-12): the record's `url.full`, with secrets hidden, else only the host. A call with no spec shows its whole address only when `urlFull` is on; `content: full` switches `urlFull` on too, for every record.
9. (Peter, 2026-11-13) The key alone switches the console exporter on, as in the OpenTelemetry Collector: `- console:` with nothing after it means `console: {}`. It is read in one pass, keeping `console` as a YAML node. 1xx replies, such as a WebSocket upgrade, have no colour, as in mitmdump, instead of the failure colour.
