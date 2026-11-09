# Own forwarder decisions (Peter, 2026-11-09)

1. The proxy forwards calls with its own forwarder, package `internal/forward`, instead of Go's `httputil.ReverseProxy`. Full replacement, no switch back.
2. The request is copied once, a shallow copy for the call's context. Go's reverse proxy made one deep copy (headers, address, trailers) and three shallow ones. A copy for the context cannot be avoided: Go only sets a request's context by copying it. Go's transport makes one more shallow copy of its own, which stays.
3. The call's context is made once, in the handler: it ignores the app leaving, carries the vendor timeout and the timing hooks.
4. The app's request is never changed: the outgoing request gets its own header map, so the record and the spec checks still read the app's original headers.
5. Contract: `Forwarder{Buffers}`, one per handler, and `Forward(ctx, w, in, vendor) error`. The vendor sender is a parameter, and it sets the vendor address, Host and headers itself before it sends (Peter, 2026-11-09). `Forward` returns an error only when no reply reached the app, and the handler then answers with 502 or 504 and `Proxy-Status`.
6. Three fixes over Go's reverse proxy, each with a scenario in `forward.feature`:
   - request trailer values reach the vendor (Go issue 82939, filed by Peter; Go sends the name with no value);
   - early replies (1xx) lose headers for one hop only, as final replies do (Go issue 81319);
   - fields forbidden after the body, such as `Content-Type`, are dropped from reply trailers (Go issues 82649 and 82450).
7. Measured gain, whole process (app, proxy and fake vendor in one test process) per call, `BenchmarkCall`, median of 9 runs of 3000 calls: 808 allocations instead of 822, and 92.3 KB allocated instead of 94.5 KB. Time per call was within the noise of the shared machine.
