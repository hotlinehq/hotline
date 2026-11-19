Feature: The level of each record
  Every record carries a level, so a log tool such as Grafana can show
  which calls went wrong. The level is the OpenTelemetry severity: its text
  and its number (INFO 9, WARN 13, ERROR 17). The record still has no
  message text: everything else is in the attributes.

  A call is INFO unless something went wrong. WARN means the call or the
  response broke the spec, or the vendor refused the call (a 4xx). ERROR means
  the vendor failed (a 5xx) or the call failed in the proxy; it wins over
  WARN.

  Rule: The level follows the vendor's answer and the checks

    Background:
      Given the shop spec

    Scenario Outline: <case>
      Given the vendor answers <answer>
      When the app sends <call>
      Then the record's level is <level>

      Examples:
        | case                         | answer                                                       | call                                                                                  | level |
        | a call that matches the spec | 201                                                          | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2} | INFO  |
        | a call that breaks the spec  | 200 and the json body {"orders":[]}                          | GET /orders?limit=abc                                                                 | WARN  |
        | a response that breaks the spec | 200 and the json body {"item":"A1","quantity":1}             | GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d                                      | WARN  |
        | the vendor refuses the call  | 400                                                          | GET /orders                                                                           | WARN  |
        | the vendor fails             | 503 and the json body {"message":"down"}                     | GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d                                      | ERROR |
        | the vendor fails and the call breaks the spec | 503 and the json body {"message":"down"}    | GET /orders/1234                                                                      | ERROR |

  Rule: A call that fails in the proxy is ERROR

    Scenario Outline: The proxy's own errors
      Given <situation>
      When an app calls through the proxy
      Then the record's level is ERROR

      Examples:
        | situation                            |
        | a vendor that refuses the connection |
        | a vendor slower than vendorTimeout   |

    Scenario: A call the proxy refuses
      Given a call the proxy refuses
      When the app sends it
      Then the record's level is ERROR

  Rule: Calls without a full record get a level too

    Scenario: A call without an integration ID
      Given a call without Proxy-Authorization
      When the call ends
      Then the record's level is INFO
