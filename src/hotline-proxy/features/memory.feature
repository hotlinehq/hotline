Feature: Memory each call uses
  The proxy hands out as little memory as it can for each call, so the
  garbage collector (the part of Go that frees unused memory) has little
  to do under load. The load test measures this properly; this scenario
  only keeps a known saving from coming undone. The limit covers all the
  memory the test run hands out: the app, the proxy and the fake vendor
  together.

  Scenario: Copying a body reuses buffers
    The proxy copies each body through a buffer it borrows from a shared
    pool and gives back, instead of making a new 32 KB buffer every call
    (Peter, 2026-11-02, found by the load test spike). Without the pool
    the calls hand out about 160 KB each, with it about 125 KB.
    Given an api with TLS origination on
    When an app sends 1000 calls of "GET https://api.talon.one/v2/x"
    Then the calls handed out less than 145 KB of memory each, on average

  Scenario: Keeping a large body reuses buffers
    The proxy keeps a copy of each body, up to 2 MiB, for its checks. It
    borrows the copy's buffer from a shared pool, in a fixed set of sizes
    from 16 KB to 2 MiB, picked by the body's Content-Length, and gives it
    back once the checks are done (Peter, 2026-11-02, found by the load
    test). The reply here is a PDF, which the spec does not check, so the
    copy is the only large thing the proxy keeps. The limit covers all the
    memory the test run hands out, the app and the fake vendor included:
    about 8,200 KB a call when each copy was grown on its own, about
    2,700 KB with the pool.
    Given the shop spec
    And the vendor answers 200 with a 1 MiB pdf body
    When the app sends GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d 200 times
    Then the calls handed out less than 4000 KB of memory each, on average
