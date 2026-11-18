Feature: WebSocket sessions
  A WebSocket starts as an ordinary call, which the vendor answers with
  101 Switching Protocols. After that, the same connection carries
  messages both ways for as long as both sides like. The proxy records the
  opening call as soon as the 101 reaches the app, like any other call, and
  the session in a record of its own when it ends, in the same trace
  (Peter, 2026-11-18, WebSocket milestone 1).

  The session record says how many bytes each side sent after the 101 and
  how long the session was open. It reads no messages: checking them comes
  in milestone 2.

  Background:
    Given a shop spec where GET /orders answers 101 Switching Protocols
    And an interception issuer named customer in certs, which the connect part picks
    And an app that trusts the interception issuer

  Scenario Outline: Every ingress gives a call record and a session record
    Given the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the <ingress> ingress
    And the app sends "hello" and reads "ok"
    Then there is one call record and one session record
    And the call record says http.response.status_code 101 and hotline.ingress <ingress>
    And the session record says hotline.ingress <ingress>

    Examples:
      | ingress       |
      | forward_proxy |
      | url_in_path   |
      | host_header   |
      | connect       |

  Scenario: A captured WebSocket gives a call record and a session record
    Given the capture listener is a socket file, whose issuer is customer too
    And the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the capture ingress
    And the app sends "hello" and reads "ok"
    Then there is one call record and one session record
    And the session record says hotline.ingress capture

  Scenario: The opening call is recorded while the session is still open
    Given the shop vendor accepts a WebSocket upgrade and keeps it open until the app closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    Then a call record with http.response.status_code 101 arrives while the WebSocket is open
    And no session record has arrived yet
    When the app closes the WebSocket
    Then there is one call record and one session record

  Scenario: The opening call ends at the 101 and has no body
    What follows the 101 is the session's, not the call's: the call's
    reply has no body, and its time ends when the 101 reached the app.
    Given the shop vendor accepts a WebSocket upgrade and keeps it open until the app closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app keeps the WebSocket open for 300 ms and closes it
    Then the call record says http.response.body.size 0, with the hash of no body
    And the call record's hotline.timing.total_us is under 300 ms
    And the session record's hotline.websocket.duration_us is at least 300 ms

  Scenario: The session record counts and hashes the bytes each way
    The bytes are those of the WebSocket's frames after the 101, as they
    passed, without TLS. Each side's hash is keyed with the session's
    trace and span, as a body's is with its call's, so the ledger covers
    what both sides said.
    Given the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app sends "hello" and reads "ok"
    Then the session record says hotline.websocket.from_app.size 5 and hotline.websocket.from_vendor.size 2
    And the session record's hotline.websocket.from_app.hash is the hash of "hello", and its hotline.websocket.from_vendor.hash of "ok"
    And the session record says hotline.integration.id talon-shop-a and server.address api.shop.test

  Scenario: The session is in the call's trace, under the call's span
    Given the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app sends "hello" and reads "ok"
    Then the session record has the call record's trace_id
    And the session record's span is a "websocket session" span under the call's "hotline call" span, starting when the call ends

  Scenario: hotline.requests counts the WebSocket once, with status 101
    The session is not counted, as a CONNECT tunnel that opens fine is not.
    Given the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app sends "hello" and reads "ok"
    Then hotline.requests counts 1 call, with http.response.status_code 101

  Scenario: A session still open at stop is ended and recorded
    Given the shop vendor accepts a WebSocket upgrade and keeps it open until the app closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the proxy stops
    Then there is one call record and one session record

  Scenario: The console shows the session's end in two lines of its own
    Given a console exporter
    And the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app sends "hello" and reads "ok"
    Then the console has 4 lines
    And the third line is the time and "talon-shop-a: WebSocket api.shop.test"
    And the fourth line is "<< ended 5 B sent 2 B received" and the time the session was open

  Scenario: hotline-proxy verify checks the session record too
    Given a file exporter writing to the folder calls
    And the shop vendor accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When an app opens a WebSocket to the shop through the forward_proxy ingress
    And the app sends "hello" and reads "ok"
    And hotline-proxy is stopped
    And I run hotline-proxy verify calls
    Then it exits with 0

  Scenario: Gemini Live's opening call checks out against its spec, with the key hidden
    The spec in specs/google names x-goog-api-key in its security section,
    so the key is in no record, span or log.
    Given the integration shop-assistant uses Gemini Live with the spec in specs/google
    And Gemini Live accepts a WebSocket upgrade, reads 5 bytes, sends "ok" and closes
    When shop-assistant opens a Gemini Live session through the connect ingress with "X-Goog-Api-Key: k-not-real-123"
    And the app sends "hello" and reads "ok"
    Then the call record says hotline.schema.status valid and url.template /ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent
    And no record, span or log holds "k-not-real-123"
