# Console exporter decisions (Peter, 2026-11-11)

Full wording in [docs/proxy-console-export.md](../proxy-console-export.md). Requirements: `console-exporter/requirements.md` in the project files.

1. A `console` exporter prints each call to standard output, modelled on mitmdump: two lines per call (request, then reply under it), and a third with what the checks found for an invalid call.
2. Basic information only: time, integration, method, address, status, failure, reply size, total time, check status. No headers, bodies, spans or metrics.
3. Colours close to mitmdump's; colour only on a terminal and without `NO_COLOR`, unless `color: always` or `never`. `color` is the only setting.
4. Long addresses are cut as mitmdump does: width less 25, at least 50; width from the terminal, then `COLUMNS`, then 80.
5. Every value from a call is escaped before printing. The proxy's logs stay on standard error.
6. One console exporter at most; lines wait in a bounded queue and are printed at once.
