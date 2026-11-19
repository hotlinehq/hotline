Feature: Choosing headers for the record
  Headers on the default list or marked in the spec are kept. Every other
  header is left out, name and all. Secrets are never recorded.

  Scenario Outline: Headers that are kept
    Given the Stripe spec
    When a call carries the header "<header>"
    Then the record has "<attribute>"

    Examples:
      | header                         | attribute                                           |
      | Content-Type: application/json | http.request.header.content-type = application/json |
      | Content-Length: 24             | http.request.header.content-length = 24             |
      | Date: Tue, 27 Oct 2026         | http.response.header.date = Tue, 27 Oct 2026        |
      | Retry-After: 5                 | http.response.header.retry-after = 5                |
      | Request-Id: req_1              | http.response.header.request-id = req_1             |
      | X-Request-Id: req-5521         | http.response.header.x-request-id = req-5521        |
      | RateLimit-Remaining: 9         | http.response.header.ratelimit-remaining = 9        |
      | Stripe-Version: 2024-06-20     | http.request.header.stripe-version = 2024-06-20     |

  Scenario: A header value that is not UTF-8 is recorded with the broken byte replaced
    OTLP text must be UTF-8, and a collector refuses a send that holds
    anything else, losing every record in it. So each byte that is not
    UTF-8 becomes the replacement character (found by fuzzing, 2026-11-18).
    Given the Stripe spec
    When a response carries Request-Id with the byte FF in it, which is not UTF-8
    Then the record has "http.response.header.request-id = req�1"

  Scenario: A header marked true keeps its name only
    Given a spec with the header X-Debug-Token marked x-hotline-redact true
    When a call carries "X-Debug-Token: abc"
    Then the record has "http.request.header.x-debug-token = REDACTED"

  Scenario: A header marked true behind a $ref keeps its name only
    A parameter can point to a shared definition elsewhere in the spec
    with $ref. Its mark counts as if written in place.
    Given a spec whose header X-Debug-Token is a $ref to a parameter marked x-hotline-redact true
    When a call carries "X-Debug-Token: abc"
    Then the record has "http.request.header.x-debug-token = REDACTED"

  Scenario: A response header marked true behind a $ref keeps its name only
    Given a spec whose response header Request-Id is a $ref to a header marked x-hotline-redact true
    When a response carries "Request-Id: user@example.com"
    Then the record has "http.response.header.request-id = REDACTED"

  Scenario: Other headers are left out
    Given the Stripe spec
    When a call carries "X-Custom: abc"
    Then the record has no attribute for X-Custom

  Scenario: Several values
    Given the Stripe spec
    When a response carries "RateLimit-Policy" twice
    Then the record has both values as a list

  Scenario Outline: Secrets are never recorded
    Given <spec>
    When a call carries "<header>"
    Then the record has no value for it

    Examples:
      | spec                                                                              | header                              |
      | a spec that marks Authorization x-hotline-redact false                            | Authorization: Bearer sk_1          |
      | the Stripe spec                                                                   | authorization: Bearer sk_1          |
      | the Stripe spec                                                                   | Proxy-Authorization: Basic dGFsb24= |
      | the Stripe spec                                                                   | Cookie: sid=1                       |
      | the Stripe spec                                                                   | Set-Cookie: sid=1                   |
      | a spec whose security section names X-Api-Key, also marked x-hotline-redact false | X-Api-Key: k1                       |
