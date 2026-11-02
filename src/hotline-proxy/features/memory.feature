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
