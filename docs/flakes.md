# Flaky tests

A flaky test is one that sometimes fails and sometimes passes on the same code. Each one is a bug: in the test, or in the proxy. It costs a rerun, and worse, it teaches everyone to rerun a red build instead of reading it (Peter, 2026-11-24).

## Rules

1. **A flaky failure is a bug.** Never rerun until green and move on. Add it to the log below (date, test, cause, fix) and fix it within a day. Never skip or turn off a test to get green.
2. **Never wait a fixed time and hope.** A test waits for something to happen with `eventually`, which checks again and again up to `waitLimit`. A wait the scenario acts out, such as a vendor that sends the rest of its answer later, uses `pause`. Both are in `src/hotline-proxy/wait_test.go`. `make lint` and CI refuse `time.Sleep` in the proxy's feature and end-to-end tests, the `src/hotline-proxy/*_test.go` files (`.github/scripts/check-test-waits.sh`).
3. **Never pick a port and let it go.** A port the system picks and the test closes can go to the next server at once. Ports the test gives the proxy, and the ports of vendors that refuse connections, come from `reservedPorts` in `src/hotline-proxy/ports_test.go`: a range the system never picks by itself, each port handed out once while the suite runs. Test processes side by side, such as fuzz workers, each start at their own block of 100. A test server listens on port 0 and stays open.
4. **Fake clock waits are named.** A step that lets fake time pass first waits until the proxy waits for what the step means, never for any pending timer (see "the response stays quiet" in `steps_forward_test.go`).
5. **Every timing branch has a test that forces it.** The coverage check needs every line. A line that runs only when timing goes one way makes the build fail now and then, so such a branch gets a test that runs it every time. When `make cover` fails, it lists the lines no test ran.

## Log

| Date | Test | Cause | Fix |
|---|---|---|---|
| 2026-11-18 | export gzip | a race in the exporter's gzip writer | PR 185 |
| 2026-11-19, 2026-11-21 | A changed config file is picked up with no signal | the start noted its files after opening its ports | PR 197 |
| 2026-11-19, 2026-11-21 | nightly FuzzFirstBytes stalled | a slow connect hung the fuzz worker | PR 198 |
| 2026-11-19 | A first message that is not a usable client hello | two listeners got the same freed port | reserved ports (rule 3) |
| 2026-11-23 | A failure is red | the refused vendor's freed port went to another server | reserved ports (rule 3) |
| 2026-11-23 | coverage of the WebSocket frame reader | a stop cut the reader before it reached a message cut short | PR 216 |
| 2026-11-23 | every package failed to build | the module proxy dropped a download | downloads first, three tries |
| 2026-11-23 | nightly race detector | the suite passed go test's 10 minute limit | `-timeout 25m` |
| 2026-11-24 | A vendor response that goes quiet | fake time passed before the proxy waited for the vendor | PR 218 |
| 2026-11-25 | nightly FuzzFirstBytes, before fuzzing | fuzz workers have back-to-back process ids, so their reserved ports started one apart and two took the same port | each process starts at its own block of 100 reserved ports |
