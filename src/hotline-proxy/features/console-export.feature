Feature: Printing every call to the console
  A console exporter prints each call to standard output as two short
  lines, like mitmdump: the request, then the reply under it. An invalid
  call gets a third line with what the checks found. The lines are
  coloured on a terminal. With --format json, each call is one line of
  JSON instead, the line a file exporter writes (Peter, 2026-11-12). The
  proxy's own logs stay on standard error (Peter, 2026-11-11).
  Requirements: console-exporter/requirements.md in the project files.

  Background:
    Given the shop spec
    And a console exporter

  Scenario: A call is a request line and a reply line
    Given the vendor answers 201
    When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
    Then the console has 2 lines
    And the first line is the time and "talon-shop-a: POST https://api.shop.test/orders"
    And the second line is "<< 201 Created 0 B", the time the call took and "valid"
    And the second line's "<<" ends just before the method on the first line

  Scenario: An invalid call has a third line with what the checks found
    Given the vendor answers 200 and the json body {"orders":[]}
    When the app sends GET /orders?limit=abc
    Then the console has 3 lines
    And the second line ends with "invalid"
    And the third line is "request: query limit type integer", starting under the method

  Scenario: A call that got no reply shows why
    Given the vendor refuses the connection
    When the app sends GET /orders
    Then the console has 2 lines
    And the second line starts with "<< 502 Bad Gateway connection_refused"

  Scenario: A call with no spec shows only its host, and no check status
    Its record has no address, as there is no spec to say what to hide.

    When a call to a host without an api ends
    Then the console has 2 lines
    And the first line ends with "talon-shop-a: PUT api.other.test"
    And the console does not hold "q=1"
    And the second line has no check status

  Scenario: A call with no spec shows its whole address when urlFull is on
    Given the not-passed entry has urlFull on
    And the app sends no integration ID
    When a call to a host without an api ends
    Then the first line ends with "not-passed: PUT https://api.other.test/v1/things?q=1"

  Scenario: A call with no spec shows its whole address under content full
    Given a file exporter
    And the not-passed entry has content full
    And the app sends no integration ID
    When a call to a host without an api ends
    Then the first line ends with "not-passed: PUT https://api.other.test/v1/things?q=1"

  Scenario: A call with a spec shows the address as its record has it, with secrets hidden
    The console never shows more of the address than the record holds
    (Peter, 2026-11-12).

    Given standard output is not a terminal, with the environment variable COLUMNS 120
    And the vendor answers 200 and the json body {"orders":[]}
    When the app sends GET /orders?limit=5&api_key=sk_live_123
    Then the first line ends with "talon-shop-a: GET https://api.shop.test/orders?limit=REDACTED&api_key=REDACTED"
    And the console does not hold "sk_live_123"

  Scenario: A console exporter written with nothing after console:
    The key alone switches it on, as in the OpenTelemetry Collector:
    "console:" with nothing after it means the same as "console: {}",
    every option at its default (Peter, 2026-11-13).
    Given the console exporter is written as "console:" with nothing after it
    When the app sends GET /orders
    Then the console has 3 lines

  Scenario: A console setting the proxy does not know is ignored, with a warning
    Given the console exporter is set with colour always
    When the app sends GET /orders
    Then the console has 3 lines
    And the proxy logged a warning with "colour" and "is not a setting and is ignored"

  Scenario: The terminal is asked its width once for each batch of calls
    Calls are recorded in the background, so an app never waits for the
    console. The console still asked the terminal its width for every
    call; now it asks once for each batch of lines it writes (review,
    Peter 2026-11-13).
    Given standard output is a terminal 100 columns wide, which takes 500 milliseconds to say so
    When the app sends GET /orders 5 times, one after another on one connection
    Then the console has 15 lines
    And standard output was asked its width fewer than 5 times

  Scenario: A console exporter alone
    Given no OTLP exporter
    When the app sends GET /orders
    Then the console has 3 lines

  Scenario: With --format json, a call is one line of JSON
    Given the program writes JSON
    And the console exporter has color always
    And the vendor answers 200 and the json body {"orders":[]}
    When the app sends GET /orders
    Then the console has 1 lines
    And the first line is the record as JSON, with the method GET and no colour

  Scenario: Plain text when standard output is not a terminal
    Given standard output is not a terminal
    When the app sends GET /orders
    Then the console has no colour

  Scenario: NO_COLOR turns the colour off
    Given standard output is a terminal 100 columns wide
    And the environment variable NO_COLOR is "1"
    When the app sends GET /orders
    Then the console has no colour

  Scenario Outline: color always and never
    Given the console exporter has color <color>
    And standard output is <where>
    When the app sends GET /orders
    Then the console <has> colour

    Examples:
      | color  | where                           | has    |
      | always | not a terminal                  | has    |
      | never  | a terminal 100 columns wide     | has no |
      | auto   | a terminal 100 columns wide     | has    |
      | auto   | a terminal 0 columns wide       | has    |

  Scenario Outline: The method and status are coloured, as mitmdump does
    Given standard output is a terminal 100 columns wide
    And the vendor answers <status>
    When the app sends <method> /orders
    Then the method <method> is bold <method colour>
    And the status <status> is bold <status colour>

    Examples:
      | method | method colour | status | status colour |
      | GET    | green         | 200    | green         |
      | DELETE | red           | 301    | magenta       |
      | POST   | magenta       | 404    | yellow        |
      | PUT    | magenta       | 503    | red           |

  Scenario: A WebSocket upgrade is not a failure, so 101 has no colour
    mitmdump, which the console follows, leaves 1xx without colour.
    Given standard output is a terminal 100 columns wide
    And a vendor that accepts a WebSocket upgrade and sends back what it gets
    When an app opens a WebSocket through the proxy and sends "ping"
    Then the status 101 is bold, with no colour

  Scenario Outline: The check status is coloured
    Given standard output is a terminal 100 columns wide
    And the vendor answers <answer>
    When the app sends <call>
    Then the check status <status> is <colour>

    Examples:
      | answer                                                         | call                                                                                 | status  | colour |
      | 201                                                            | POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2} | valid   | green  |
      | 400                                                            | GET /orders?limit=abc                                                                | invalid | red    |
      | 200 with "Content-Type: text/event-stream" and the body data: x | GET /orders                                                                          | skipped | dim    |

  Scenario: The findings are dim, with the side in red
    Given standard output is a terminal 100 columns wide
    And the vendor answers 200 and the json body {}
    When the app sends GET /orders?limit=abc
    Then the third line is dim, with "request" in red

  Scenario: A failure is red
    Given standard output is a terminal 100 columns wide
    And the vendor refuses the connection
    When the app sends GET /orders
    Then "connection_refused" is red

  Scenario: A slow call's time is yellow
    Given standard output is a terminal 100 columns wide
    And the shop vendor sends half of its reply, and the rest 1100 milliseconds later
    When the app sends GET /orders
    Then the time the call took is yellow
    And the second line has the reply size 2.0 KiB

  Scenario Outline: A long address is cut to the terminal's width, as mitmdump does
    The address may take the width less 25 characters, and at least 50.
    Some terminals, often in containers and CI, say they are 0 columns
    wide: that counts as not knowing the width.
    Given standard output is <where>
    When the app sends GET /orders with a query 200 characters long
    Then the address on the first line keeps its first <length> characters, then "…"

    Examples:
      | where                                                                | length |
      | a terminal 100 columns wide                                          | 75     |
      | a terminal 60 columns wide                                           | 50     |
      | not a terminal, with the environment variable COLUMNS 120            | 95     |
      | not a terminal                                                       | 55     |
      | a terminal 0 columns wide, with the environment variable COLUMNS 120 | 95     |
      | a terminal 0 columns wide                                            | 55     |

  Scenario Outline: Characters that could change the terminal are escaped
    Given the not-passed entry has urlFull on
    And the app sends no integration ID
    When a call to a host without an api ends, with <what> in the query
    Then the first line shows it as <shown>
    And the console holds no character that could change the terminal

    Examples:
      | what                       | shown  |
      | the control character 9B   | \u009b |
      | the byte FF, not UTF-8     | \xff   |
      | the right-to-left mark 202E | \u202e |

  Scenario Outline: With --format json, characters that could change the terminal are escaped
    JSON itself only escapes the characters below 20, so the console
    escapes the rest of the control and direction characters too.

    Given the program writes JSON
    And the vendor answers 200 with <what> in the X-Request-Id header
    When the app sends GET /orders
    Then the console has 1 lines
    And the console holds no character that could change the terminal

    Examples:
      | what                        |
      | the control character 9B    |
      | the right-to-left mark 202E |
      | the language tag E0001      |

  Scenario: A write that fails is counted
    Given standard output is closed
    When the app sends GET /orders
    Then the call succeeds
    And the proxy warns that the export failed

  Scenario: A program that starts the proxy without saying where the console is
    A Go program can start the proxy without filling in the console
    option. A console exporter in the config then prints nothing, and
    the proxy still runs and answers calls.
    Given the program starting the proxy gives no console
    When the app sends GET /orders
    Then the call succeeds
    And the console has 0 lines

  Scenario Outline: Wrong console exporter settings
    Given the console exporter is set with <setting>
    When the proxy loads the config
    Then the proxy refuses the config, saying <problem>

    Examples:
      | setting                    | problem                                                                          |
      | color loud                 | color must be auto, always or never                                              |
      | a second one               | there may be only one console exporter                                           |
      | a word instead of settings | console exporter: write its settings as color: auto, or nothing for the defaults |
      | a file path as well        | an exporter is only one of otlp, file or console                                 |
