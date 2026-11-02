# Trace context decisions (Peter, 2026-10-31)

Full wording in [docs/proxy-trace-context.md](../proxy-trace-context.md).

1. Each record carries the app's trace id and flags in the log record's own trace fields, never in attributes of our own.
2. The span id is a new one for the proxy's step; the app's span id is not recorded.
3. No trace id, or one the proxy can't read, gets a new one, so every record has one.
4. `traceFormat` per integration: `w3c` (default), `b3`, `xray` or `jaeger`.
5. `traceForward` per integration, off by default, sends our trace id and span id to the vendor.
6. The trace id, span id and flags are signed with the record.
7. No traces (spans) are sent for now; the call timeline design waits in the project files.
