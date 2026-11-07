# Own OTLP encoder for records and traces: scope (draft)

Status: draft for Peter to read. Nothing here is built, and no contract changes until Peter agrees (2026-11-07).

OTLP is the OpenTelemetry protocol: the standard way to ship records, counts and traces to a collector. Protobuf is the compact binary format OTLP uses on the wire. An encoder is the code that turns data into those bytes.

## The problem

At 5000 calls a second on 2 cores the proxy runs out of processor time. In the load test's small JSON call, about 10% of the proxy's processor time goes to the OpenTelemetry log library copying and checking each record's attributes, about 5% to its protobuf encoding and about 6% to gzip. With the trace library and the garbage collector (the part of Go that frees memory) the whole OpenTelemetry cost is about 35 to 40%. These figures come from the load test thread's profiles; the spike re-measures them (see below).

Today each call goes through these steps: the proxy builds a list of attributes, the library copies it into its own record (and copies it again when queueing), the queue holds the records until a send, and the library then converts them to protobuf structures, encodes them and gzips them. Spans go the same way through the trace library.

## The idea

Write the two hot messages, the call record and the call's trace, straight into OTLP protobuf bytes, once, into reused buffers. The queue then holds ready bytes instead of library objects. A send joins the waiting bytes into one request, compresses it and posts it. The wire format stays standard OTLP, so every collector keeps working.

Why joining bytes works: an OTLP request is a resource (the sender's labels), holding one scope (the sending code's name), holding a repeated list of records. In protobuf, a repeated field is just the same field written again and again, so already encoded records can be placed one after another behind a small header. A record is encoded once, and every destination reuses the same bytes. Only the header differs, because each destination has its own resource labels, and that header is made once per destination.

## In scope

- Messages: the `hotline.request` log record (full and minimal) and the spans of a call's trace, exactly as in [otel-export.md](otel-export.md): same fields, names, types, severity rule and signature.
- Transports: OTLP over gRPC and over HTTP as protobuf, for these two messages. Keeping what exists: TLS, `headers`, `compression`, the 10 second limit, retries on a busy collector, the 512 per send, `queueSize` with drop-the-oldest, the intervals, flush at shutdown, and the `hotline.exporter.*` numbers.
- The one thing the library did that we then do ourselves: retries, honoring the collector's wait hint, and partial success (the collector took some records and refused others).
- Compression: gzip with a reused, cheaper-to-run writer.
- Tests that prove the bytes are standard OTLP (next section) and a benchmark that proves the gain.

## Out of scope

- The counter `hotline.requests` and the proxy's own numbers for the `telemetry` destination. They stay on the normal OpenTelemetry metrics library, as do startup and admin messages. OpenTelemetry stays the facade (the front door of the code) for everything except these two hot messages. If the counter shows up in the profile after the rest is fixed, it gets its own proposal.
- Any config change, any change of a field or its meaning, any change of the signature rules.
- Making the trace optional. A product choice for Peter, listed below. The encoder keeps it a simple on or off switch, but the switch is not built here.
- Grafana Alloy, or any sidecar that does the encoding for us.
- Logs of the proxy itself, and new metrics.

## Proving it is standard OTLP

1. **Official decoder.** The repo already depends on `go.opentelemetry.io/proto/otlp`, the official generated types. Every test encodes with the new code and decodes with those types, so a wrong field number or wire type fails at once.
2. **Same meaning as today.** For the same input, send once through the current library path and once through the new encoder, decode both, and compare the decoded messages, not the raw bytes (field order may differ). Inputs: every attribute type the proxy uses (text, whole number, true or false, lists of text, lists of maps for `hotline.schema.*.errors`), empty and very long text, text that is not valid UTF-8, zero time, no trace id, all severities, minimal and full records, spans with and without a parent, the three span kinds, and ok, error and unset status. A fuzz test (random inputs, run for a short while) covers the rest.
3. **A real collector.** Run the repo's `otelcol-dev` build with an OTLP receiver and send batches over gRPC and HTTP, with and without gzip. It must accept all of them with no partial success.
4. **The existing behaviour tests.** The godog feature files already check records and traces through a fake OTLP receiver at the proxy's edge. They must pass unchanged, which proves nothing visible moved.
5. **Reuse safety.** A race test (`go test -race`) with many calls at once, and a test that holds a queue full and checks a buffer is never reused while it waits to be sent.

## Expected gain

A guess, to be replaced by the spike's numbers: the log copy and checks (10%) and the encoding (5%) mostly go away, the trace library cost goes away similarly, gzip gets cheaper with a reused writer and a faster level, and the garbage collector has less to do because bytes in a queue are one block each and not many small objects. Honest range: 15 to 25% less proxy processor time on the small call at 5000 a second, which would move the CPU limit from about 5000 to about 6000 or 6500 calls a second. Large calls gain little, since their cost is in bodies.

## Risks

- **We own the transport.** Retries, throttling hints, partial success and gRPC details are bugs we could write. Mitigation: keep grpc-go and the standard HTTP client, send bytes through them, and test against a fake collector that refuses, throttles and half accepts.
- **Drift from the standard.** New fields appear in OTLP (the record's event name is recent). Mitigation: the official types in every test, and the pinned version in `go.mod`.
- **Silent difference in meaning** from the library, such as how it maps a value or limits attributes. Mitigation: the decoded comparison above.
- **Buffer reuse bugs**: a buffer given back while still queued corrupts another call's record. Mitigation: the queue owns the bytes until sent; race test.
- **More code to maintain** in place of a library others maintain. About 600 to 900 lines estimated. Worth it only if the gain clears the bar below.
- **Memory**: 10 000 queued encoded records per destination is about 10 MB at 1 KB each, probably less than the objects held today; measured in the spike.

## Spike plan

Throwaway code on a branch, not merged, to answer one question: is the gain worth it?

1. **Encoder only, in a Go benchmark.** Encode the repo's `benchRecord` (in `record_bench_test.go`, about as large as the load test's small call) and its spans. Compare, per record: processor time, bytes allocated, allocations, and encoded size before and after gzip, against today's path (library record, clone, protobuf conversion, marshal).
2. **Whole proxy.** Wire the encoder to the HTTP transport only, keep gRPC on the old path, and run the load test's small call at 5000 a second under `taskset` on two cores (`HOTLINE_LOAD_PROXY_CPUS=0,1`), main against branch, three repeats each, taking turns (`make load-compare`). Report processor microseconds per call, KB allocated per call, extra p99 delay, and peak memory, each repeat as it came.
3. **Profile after.** One processor profile of the branch, to see what the next biggest cost is, so Peter sees the whole picture and not only this part.
4. **Go or no-go** against the bar Peter sets.

If the answer is go, the real work follows the TDD loop of the repo: the compatibility tests first, then the encoder, then the transports, then a pull request.

## Open questions for Peter

1. May I start the spike (throwaway code, benchmark only, nothing merged)?
2. What gain makes it worth the extra code? Suggestion: at least 15% less proxy processor time on the small call at 5000 a second, and no worse on the large calls.
3. Gzip: may I use the fastest gzip level and, if it helps, the faster `klauspost/compress` library? It is a new dependency, and the output stays standard gzip.
4. We would drop the OpenTelemetry log and trace exporters, keeping grpc-go for the connection. Is that fine, as long as the config stays the same?
5. Trace optional: decide now, or after the spike shows what a trace costs once it is cheap? Suggestion: after the spike. Note that the record's `hotline.trace.signature` covers the trace, so a trace switch changes the signature contract and needs Peter's approval first.
6. Replace the old path outright after the tests pass, or keep it behind a hidden switch for one release? Suggestion: replace outright, so there are not two paths to keep correct.
