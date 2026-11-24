# Tests in CI

- `make test` runs no test with the race detector (Peter, 2026-11-11). With coverage on, it made CI too slow: the metrics tests went from seconds to minutes. The race detector found one real race before it was removed, the body's end running twice on a WebSocket (#139); run `go test -race` by hand when changing code that runs on several goroutines.
- The metrics packages (`src/hotline/...`) measure only their own coverage; the proxy packages measure coverage of both modules.
- No flaky tests: the rules (wait with `eventually` or `pause`, never `time.Sleep`; ports from `reservedPorts`; a test for every timing branch; new scenarios run 10 times on a pull request) and the log of flakes found are in [docs/flakes.md](../flakes.md) (Peter, 2026-11-24).
- The test world picks new ports and starts the proxy again, up to three times, when a port it picked itself is taken (#139), as the end-to-end test does (#115).
- The path probe's tests send a real ping on loopback, through the ICMP socket an ordinary user may open (#208). Linux allows it only for the groups in `net.ipv4.ping_group_range`: Docker allows every group, but GitHub's Ubuntu runner and a plain Linux machine allow none. The test and fuzz workflows set `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` first; do the same before `make test` on Linux outside Docker. macOS needs nothing.
