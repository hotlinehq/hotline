# Fuzzing

Fuzzing makes up input after input, changing each one a little, and keeps any input that breaks a rule. It is Go's own `go test -fuzz`. Requirements: `fuzzing/requirements.md` in the Hotline project files (Peter, 2026-11-18).

1. **Three rules for every input.** The proxy never panics (stops on an error such as reading past the end of a list), it answers within 2 seconds, and a normal call still works after it. A body's record must also arrive within the 2 seconds, its spec check included.
2. **Both ways in (Peter, 2026-11-18).**
   - *Through the proxy's ways in*, in `src/hotline-proxy/fuzz_test.go`: the real proxy with the test world's fakes, sent input the way an app or a vendor sends it. These follow the testing rule, and `go test` runs their saved inputs (the seeds and `testdata/fuzz/`), in well under a second.
     - `FuzzFirstBytes`: the first bytes on the transparent listener, and after a capture CONNECT on the capture socket file.
     - `FuzzRequest`: a raw request on the proxy port.
     - `FuzzRequestBody`: a JSON, form or XML body checked against a spec.
     - `FuzzReply`: a vendor's reply, content type and body.
     - `FuzzVerify`: the files `hotline-proxy verify` reads.
   - *Straight at the parsers*, in `fuzz_parsers_test.go` and a `fuzz_test.go` in `internal/spec/read`, `internal/santhosh` and `internal/export`: `FuzzReadHello`, `FuzzSentID`, `FuzzForm`, `FuzzXML`, `FuzzDecodeJSON`, `FuzzReadLine`. They call inner functions, a written exception to the testing rule for fuzzing only. To keep it from spreading, they are built only with the `fuzz` tag, so `make test`, CI, lint and the coverage count never see them. An input they find gets a behaviour test with its fix. A parser that moves takes its fuzz test along, or the test is deleted: it is no contract.
3. **Nightly.** `.github/workflows/fuzz.yml` runs each target for 5 minutes at 01:00 UTC (02:00 in Prague in winter), on GitHub's own machines, which cost nothing for this public repository, and by hand from the Actions tab. A failing job saves the input as the artifact `fuzz-<target>`; nothing is pushed. A person adds the input to `testdata/fuzz/<target>/` with the fix.
4. **Found on the first runs (2026-11-18):**
   - A form body whose names are all numbers, such as `0=x`, stopped the whole proxy (spec-checks decision 17).
   - A header value that is not UTF-8 made the collector refuse the whole send, losing every record in it. Every text of a record is now made valid UTF-8 before the ledger hashes it (otlp-encoder decision).
