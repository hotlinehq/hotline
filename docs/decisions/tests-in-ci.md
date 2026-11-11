# Tests in CI

- `make test` runs no test with the race detector (Peter, 2026-11-11). With coverage on, it made CI too slow: the metrics tests went from seconds to minutes. The race detector found one real race before it was removed, the body's end running twice on a WebSocket (#139); run `go test -race` by hand when changing code that runs on several goroutines.
- The metrics packages (`src/hotline/...`) measure only their own coverage; the proxy packages measure coverage of both modules.
- The test world picks new ports and starts the proxy again, up to three times, when a port it picked itself is taken (#139), as the end-to-end test does (#115).
