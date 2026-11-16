# Agreed with Peter, 2026-10-31. Detail: docs/proxy-trace-context.md.
Feature: Records carry the app's trace context
  A trace is all the steps of one request, across services, tied together
  by one trace id. When the app's call carries a trace id, the proxy puts
  it on the call's record, so Grafana can show the record next to the
  app's own logs and traces.

  The ids go in the log record's own fields, not in attributes:
  trace_id, span_id and flags, as OpenTelemetry defines them. A span is one
  step of a trace; the proxy's step gets a new span id of its own. The
  app's span id is not on the record; it is the parent of the call's
  trace (timing.feature). The flags say whether
  the trace is sampled, that is kept by whoever records traces.

  Each integration picks the format its app sends with traceFormat: w3c,
  the default, b3, xray or jaeger. When there is no trace id, or the proxy
  cannot read it, the proxy makes a new one, so every record has one.

  The proxy only reads the trace headers. With traceForward on, which is
  off by default, it also sends its own trace id and the "vendor call" span's id to the
  vendor, so the vendor's own spans sit under that step.

  The trace id and span id are covered by the record's hash, with the rest
  of the record: see ledger.feature.

  Background:
    Given the shop spec

  Rule: The app's trace id goes on the record

    Scenario Outline: Each format is read
      Given the integration's traceFormat is <format>
      When the app sends GET /orders with "<headers>"
      Then the record's trace_id is <trace id> and its flags are <flags>
      And the record's span_id is new

      Examples:
        | format | headers                                                                                                                          | trace id                         | flags |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01                                                             | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00                                                             | 4bf92f3577b34da6a3ce929d0e0e4736 | 00    |
        | w3c    | traceparent: 01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-later                                                       | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1                                                                          | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-d-00f067aa0ba902b6                                                         | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | b3: a3ce929d0e0e4736-00f067aa0ba902b7-0                                                                                          | 0000000000000000a3ce929d0e0e4736 | 00    |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7                                                                            | 4bf92f3577b34da6a3ce929d0e0e4736 | 00    |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Sampled: 1                                   | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Sampled: true                                | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Flags: 1                                     | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7                                                    | 4bf92f3577b34da6a3ce929d0e0e4736 | 00    |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1                                      | 5759e988bd862e3fe1be46a994272793 | 01    |
        | xray   | X-Amzn-Trace-Id: Sampled=0;Self=1-67891234-12456789abcdef012345678;Root=1-5759e988-bd862e3fe1be46a994272793                     | 5759e988bd862e3fe1be46a994272793 | 00    |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:0:1                                                             | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |
        | jaeger | uber-trace-id: abc:def:0:0                                                                                                       | 00000000000000000000000000000abc | 00    |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736%3A00f067aa0ba902b7%3A0%3A3                                                       | 4bf92f3577b34da6a3ce929d0e0e4736 | 01    |

    Scenario: With no traceFormat, the app's traceparent is read
      When the app sends GET /orders with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the record's trace_id is 4bf92f3577b34da6a3ce929d0e0e4736 and its flags are 01

    Scenario: A call without an integration ID is read as w3c
      Given the app sends no integration ID
      When the app sends GET /orders with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the record's trace_id is 4bf92f3577b34da6a3ce929d0e0e4736 and its flags are 01

    Scenario: The not-passed entry may pick the format for calls without an integration ID
      Given a not-passed entry with traceFormat b3
      And the app sends no integration ID
      When the app sends GET /orders with "b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1"
      Then the record's trace_id is 4bf92f3577b34da6a3ce929d0e0e4736 and its flags are 01

    Scenario: A call the proxy refuses carries the app's trace id too
      When an app sends "GET /v2/x" with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the record's trace_id is 4bf92f3577b34da6a3ce929d0e0e4736 and its flags are 01

  Rule: Without a trace id the proxy can read, it makes a new one

    Scenario Outline: A missing or broken trace context gets a new trace id
      Given the integration's traceFormat is <format>
      When the app sends GET /orders with the trace headers "<headers>"
      Then the record has a new trace_id, with flags 01
      And the record's span_id is new

      Examples:
        | format | headers                                                                                                                 |
        | w3c    |                                                                                                                      |
        | w3c    | traceparent: ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01                                             |
        | w3c    | traceparent: 00-00000000000000000000000000000000-00f067aa0ba902b7-01                                             |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01                                             |
        | w3c    | traceparent: 00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01                                             |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-later                                       |
        | w3c    | traceparent: 01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01later                                        |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7                                                |
        | w3c    | traceparent: 00_4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7_01                                             |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01; traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4737-00f067aa0ba902b7-01    |
        | w3c    | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1                                                          |
        | b3     |                                                                                                                      |
        | b3     | b3: 0                                                                                                            |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-x                                                          |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-1                                                           |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b-1                                                           |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1-00f067aa0ba902b                                          |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1-00f067aa0ba902b6-more                                    |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-Sampled: 1                                                  |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Sampled: maybe               |
        | xray   |                                                                                                                      |
        | xray   | X-Amzn-Trace-Id: Parent=53995c3f42cd8ad8;Sampled=1                                                               |
        | xray   | X-Amzn-Trace-Id: Root=2-5759e988-bd862e3fe1be46a994272793                                                        |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988bd862e3fe1be46a994272793                                                         |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988-bd862e3fe1be46a99427279                                                         |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad                                 |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988-bd862e3fe1be46a994272793;Sampled=yes                                            |
        | jaeger |                                                                                                                      |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:0                                               |
        | jaeger | uber-trace-id: 0:00f067aa0ba902b7:0:1                                                                            |
        | jaeger | uber-trace-id: 04bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:0:1                                            |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736:000f067aa0ba902b7:0:1                                            |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:0:1ff                                           |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736%zz00f067aa0ba902b7:0:1                                           |

    Scenario: Each call gets its own new trace id
      When the app sends two calls without a trace header
      Then their records have different trace_ids

    Scenario: A new X-Ray trace id starts with the call's time
      Given the integration's traceFormat is xray
      When the app sends GET /orders
      Then the record's trace_id starts with the call's time in seconds, as X-Ray needs

  Rule: The call goes to the vendor unchanged, unless traceForward is on

    Scenario: By default the vendor gets the app's trace header as it was sent
      When the app sends GET /orders with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the vendor gets the trace header "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

    Scenario: By default no trace header is added
      When the app sends GET /orders
      Then the vendor gets no traceparent header

    Scenario Outline: With traceForward on, the vendor gets the proxy's own step
      Given the integration's traceForward is on, with traceFormat <format>
      When the app sends GET /orders with "<headers>"
      Then the vendor gets <sent>, with the record's trace_id and the "vendor call" span's id
      And the vendor gets none of <gone>

      Examples:
        | format | headers                                                                                                              | sent                                                         | gone        |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01                                                 | traceparent: 00-TRACE-SPAN-01                                | b3          |
        | w3c    | traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00                                                 | traceparent: 00-TRACE-SPAN-00                                | b3          |
        | b3     | b3: 4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1-00f067aa0ba902b6                                             | X-B3-TraceId: TRACE; X-B3-SpanId: SPAN; X-B3-Sampled: 1      | b3, X-B3-ParentSpanId, X-B3-Flags |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Flags: 1                         | X-B3-TraceId: TRACE; X-B3-SpanId: SPAN; X-B3-Sampled: 1      | X-B3-Flags  |
        | b3     | X-B3-TraceId: 4bf92f3577b34da6a3ce929d0e0e4736; X-B3-SpanId: 00f067aa0ba902b7; X-B3-Sampled: 0                       | X-B3-TraceId: TRACE; X-B3-SpanId: SPAN; X-B3-Sampled: 0      | b3          |
        | xray   | X-Amzn-Trace-Id: Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1                          | X-Amzn-Trace-Id: Root=1-TRACE;Parent=SPAN;Sampled=1          | b3          |
        | jaeger | uber-trace-id: 4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:0:1                                                 | uber-trace-id: TRACE:SPAN:0:1                                | b3          |

    Scenario: With traceForward on and no trace id from the app, the vendor gets the new one
      Given the integration's traceForward is on, with traceFormat w3c
      When the app sends GET /orders
      Then the vendor gets traceparent: 00-TRACE-SPAN-01, with the record's trace_id and the "vendor call" span's id
