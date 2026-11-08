# Answers to the open questions are in docs/proxy-spec-checks.md.
Feature: Checking calls against the vendor's spec
  Every part of a call is checked against our copy of the vendor's OpenAPI
  spec: path values, query values, headers, cookies and the body, and in
  the reply the status, headers and body. The checks run after the reply
  has gone back to the app and never change it.

  Each result is a structured entry with the part, the place, the rule and
  what the spec expects, never a value from the call. A result is written
  here as "part place rule expected"; several are joined by "; ".

  The request is checked as soon as it has been sent, while the proxy
  waits for the vendor, and the reply as soon as it has arrived, while the
  proxy sends it on: never in the call's way (Peter, 2026-10-31).

  The check's status, hotline.schema.status, is on the record: valid when
  nothing was found, invalid when the call or the reply broke the spec,
  and skipped when nothing was found but part of the check did not run.
  The trace has a "request spec check" and a "reply spec check" span, with
  no attribute. Each is ok when its side passed and error when it broke
  the spec; a skipped check leaves it unset, since nothing was proven
  (Peter, 2026-10-31).

  Background:
    Given the shop spec

  Rule: The status of the check

    Scenario Outline: The status is on the record, and each span says how its side went
      Given the vendor answers <answer>
      When the app sends <call>
      Then the record says hotline.schema.status=<status>
      And the "request spec check" span ends with status <request>
      And the "reply spec check" span ends with status <reply>

      Examples:
        | answer                                                         | call                                                                                 | status  | request | reply |
        | 201                                                            | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2} | valid   | ok      | ok    |
        | 400                                                            | GET /orders?limit=abc                                                                | invalid | error   | ok    |
        | 418                                                            | GET /orders                                                                          | invalid | ok      | error |
        | 201                                                            | POST /orders with "Idempotency-Key: k1" and a json body of 3 MiB                     | skipped | unset   | ok    |
        | 200 with "Content-Type: text/event-stream" and the body data: x | GET /orders                                                                          | skipped | ok      | unset |

    Scenario: A call that got no reply has no reply check
      Given the vendor refuses the connection
      When the app sends GET /orders
      Then the "request spec check" span ends with status ok
      And the trace has no "reply spec check" span

    Scenario Outline: An error answer from the vendor fails no span
      Only a breakdown fails a span: a timeout, a lost connection, a
      failed TLS setup. The vendor's own answer, even a 5xx, does not
      (Peter, 2026-10-31).
      Given the vendor answers <status>
      When the app sends GET /orders
      Then the "vendor call" span ends with status unset
      And the "hotline call" span ends with status unset

      Examples:
        | status |
        | 404    |
        | 503    |

  Rule: Every part of the request is checked

    Scenario: A call that matches the spec has no results
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then the app got the vendor's answer
      And the record has no request results
      And the record has no reply results

    Scenario Outline: A mistake in one part is reported in that part
      When the app sends <call>
      Then the request results are <results>

      Examples:
        | call                                                                    | results                              |
        | GET /orders?limit=abc                                                   | query limit type integer             |
        | GET /orders?limit=500                                                   | query limit maximum 100              |
        | GET /orders?lmit=5                                                      | query lmit not_in_spec               |
        | GET /orders?tag=new&tag=old                                             | query tag[1] enum new, paid          |
        | GET /orders?filter[status]=lost                                         | query filter.status enum open, closed |
        | GET /orders with "Cookie: session=abc"                                  | cookie session type integer          |
        | GET /orders/1234                                                        | path orderId format uuid             |
        | POST /orders and the json body {"item":"A1","quantity":2}               | header Idempotency-Key required      |
        | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1"} | body $.quantity required             |
        | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":0} | body $.quantity minimum 1 |
        | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":1,"note":"x"} | body $.note additionalProperties |
        | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":1,"lines":[{"sku":"a"},{}]} | body $.lines[1].sku required |

    Scenario: Results never hold values from the call
      When the app sends GET /orders?limit=secret-value&filter[status]=other-secret
      Then no result holds secret-value or other-secret

    Scenario: Several mistakes are all reported
      When the app sends GET /orders?limit=abc&lmit=5
      Then the request results are query limit type integer; query lmit not_in_spec

    Scenario: Headers the spec doesn't name are ignored
      When the app sends GET /orders with "User-Agent: shop/1; traceparent: 00-abc-def-01"
      Then the record has no request results

    Scenario: Calls are matched to the route before they are checked
      When the app sends GET /nowhere
      Then the record says hotline.schema.skipped=no_spec_route

  Rule: The body's type must be one the spec lists

    Scenario: A content type the spec doesn't list is reported
      When the app sends POST /payments and the json body {"amount":1,"currency":"eur"}
      Then the request results are body $ content_type application/x-www-form-urlencoded

    Scenario: A body where the spec expects none is reported
      When the app sends GET /orders and the json body {"item":"A1"}
      Then the request results are body $ not_in_spec

    Scenario: A missing required body is reported
      When the app sends POST /orders with "Idempotency-Key: k1"
      Then the request results are body $ required

    Scenario: A body that is not valid JSON is reported
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":
      Then the request results are body $ json

    Scenario: A listed format the proxy can't read is skipped
      When the app sends PUT /files/a.pdf and the pdf body %PDF-1.7
      Then the record says hotline.schema.skipped=unsupported_content_type
      And the record has no request results

    Scenario: A body larger than 2 MiB is not checked
      When the app sends POST /orders with "Idempotency-Key: k1" and a json body of 3 MiB
      Then the record says hotline.schema.skipped=body_too_large
      And the record has no request results

  Rule: Form bodies are read the way Stripe writes them

    Scenario Outline: A form body is checked like JSON
      When the app sends POST /payments and the form body <body>
      Then the request results are <results>

      Examples:
        | body                                                    | results                               |
        | amount=abc&currency=eur&metadata[order]=6735            | body $.amount type integer            |
        | amount=5                                                | body $.currency required              |
        | amount=5&currency=eur&items[0][price]=p1&items[1][quantity]=x | body $.items[1].quantity type integer |

    Scenario: A valid form body has no results
      When the app sends POST /payments and the form body amount=5&currency=eur&metadata[order]=6735&items[0][price]=p1&items[0][quantity]=2
      Then the record has no request results

  Rule: Formats are checked

    Scenario Outline: A wrong format is reported
      When the app sends GET /formats?<name>=<wrong>
      Then the request results are query <name> format <name>

      Examples:
        | name      | wrong               |
        | date-time | 2026-13-45T00:00:00Z |
        | date      | yesterday           |
        | email     | not-an-email        |
        | uuid      | 1234                |
        | uri       | no-scheme           |
        | ipv4      | 300.1.1.1           |
        | ipv6      | zz::1               |
        | int32     | 3000000000          |
        | int64     | 9223372036854775808 |

    Scenario Outline: Other formats are ignored
      When the app sends GET /formats?<name>=<value>
      Then the record has no request results

      Examples:
        | name      | value       |
        | hostname  | -not-a-host |
        | stripe-id | anything    |

  Rule: The login key must be present, and its value is never read

    Scenario: A missing login key is reported
      When the app sends GET /orders without the login key
      Then the request results are header Authorization required

    Scenario: A route open to all needs no login key
      When the app sends GET /public without the login key
      Then the record has no request results

  Rule: The reply is checked the same way

    Scenario: A reply body that breaks the spec is reported
      Given the vendor answers 200 with "X-Request-Id: r1" and the json body {"item":"A1"}
      When the app sends GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d
      Then the reply results are body $.quantity required

    Scenario: A missing required reply header is reported
      Given the vendor answers 200 and the json body {"item":"A1","quantity":1}
      When the app sends GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d
      Then the reply results are header X-Request-Id required

    Scenario: A status the spec doesn't list is reported, and the reply is not checked further
      Given the vendor answers 418 and the json body {"nonsense":true}
      When the app sends GET /orders
      Then the reply results are status 418 not_in_spec

    Scenario: A default reply matches any status
      Given the vendor answers 503 and the json body {}
      When the app sends GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d
      Then the reply results are body $.message required

    Scenario: A streaming reply is not checked
      Given the vendor answers 200 with "Content-Type: text/event-stream" and the body data: x
      When the app sends GET /orders
      Then the record says hotline.schema.skipped=streaming
      And the record has no reply results

  Rule: At most 20 results each way

    Scenario: Many mistakes are capped, with the total
      When the app sends POST /orders with "Idempotency-Key: k1" and an order with 25 lines without a sku
      Then the record has 20 request results
      And the record says hotline.schema.request.errors_total=25

    Scenario: The total is left out when nothing was cut
      When the app sends POST /orders with "Idempotency-Key: k1" and an order with 3 lines without a sku
      Then the record has 3 request results
      And the record has no hotline.schema.request.errors_total

  Rule: Calls without a known integration are not checked

    Scenario: A not-passed call has no results
      Given the app sends no integration ID
      When the app sends GET /orders?limit=abc
      Then the record has no request results

  Rule: Which parts run is a list on the api

    The api names the parts to check in checks: path, query, headers,
    cookies and body for the request, and status, reply-headers and
    reply-body for the reply. Finding the route and its template always
    happens. Leaving checks out checks every part; checks: [] finds the
    route only. A part left out leaves no result and no skipped reason
    (Peter, 2026-11-08).

    Scenario Outline: A part left out is not checked
      Given the shop spec with checks <picked>
      And the vendor answers <answer>
      When the app sends <call>
      Then the record has no request or reply results
      And the record says hotline.schema.status=valid

      Examples:
        | left out      | picked                                                       | answer                                              | call                                                                    |
        | path          | [query, headers, cookies, body, status, reply-headers, reply-body] | 200 with "X-Request-Id: r1" and the json body {"item":"A1","quantity":1} | GET /orders/1234 |
        | query         | [path, headers, cookies, body, status, reply-headers, reply-body] | 200                                                 | GET /orders?limit=abc                                                   |
        | headers       | [path, query, cookies, body, status, reply-headers, reply-body] | 201                                                 | POST /orders and the json body {"item":"A1","quantity":2}               |
        | cookies       | [path, query, headers, body, status, reply-headers, reply-body] | 200                                                 | GET /orders with "Cookie: session=abc"                                  |
        | body          | [path, query, headers, cookies, status, reply-headers, reply-body] | 201                                                 | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1"} |
        | status        | [path, query, headers, cookies, body, reply-headers, reply-body] | 418                                                 | GET /orders                                                             |
        | reply-headers | [path, query, headers, cookies, body, status, reply-body]    | 200 and the json body {"item":"A1","quantity":1}    | GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d                        |
        | reply-body    | [path, query, headers, cookies, body, status, reply-headers] | 200 with "X-Request-Id: r1" and the json body {"item":"A1"} | GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d |

    Scenario: A part in the list is still checked
      Given the shop spec with checks [query]
      When the app sends GET /orders?limit=abc with "Cookie: session=abc"
      Then the request results are query limit type integer

    Scenario: A reply part in the list is still checked
      Given the shop spec with checks [reply-body]
      And the vendor answers 200 with "X-Request-Id: r1" and the json body {"item":"A1"}
      When the app sends GET /orders/1234
      Then the record has no request results
      And the reply results are body $.quantity required

    Scenario: An empty list finds the route only
      Given the shop spec with checks []
      And the vendor answers 418
      When the app sends POST /orders and the json body {"item":"A1"}
      And the record has url.template /orders
      And the record has no request or reply results
      And the record has no hotline.schema.status
      And the trace has no "request spec check" span
      And the trace has no "reply spec check" span

    Scenario: A large body is not even looked at when no body is picked
      A body over 2 MiB is skipped when the body is checked. With no body
      picked there is nothing to skip, and the proxy keeps no copy of it.
      Given the shop spec with checks [path, query, headers, cookies]
      And the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and a json body of 3 MiB
      Then the record has no request or reply results
      And the record has no hotline.schema.skipped
      And the record says hotline.schema.status=valid

    Scenario: An unknown check name stops the proxy at start
      Given the shop spec with the unknown check "queery"
      When the proxy loads the config
      Then the config is refused with a message naming the problem
