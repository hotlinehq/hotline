# Agreed with Peter, 2026-10-31. Detail: docs/proxy-traces.md.
Feature: Each call's timings are a trace
  A trace is a set of spans; a span is one step with a start time and an
  end time. Every call gets one trace, sent over OTLP to every exporter
  next to its record, so Grafana can draw the call as a timeline (Peter,
  2026-10-31). Every call gets one: there is no sampling.

  The call span, "hotline call", runs from the moment the call reached the
  proxy until its last byte went back to the app. Under it are, in order:
  "routing" (finding the ingress, the integration and the vendor),
  "vendor call" and "reply to app" (sending the last bytes to the app).
  For a call with a full record, "request spec check" and "reply spec
  check" are under it too: the checks against the spec, which run
  alongside the call, so they may end after it (Peter, 2026-10-31).
  Under "vendor call" are "dns lookup", "tcp connect", "vendor tls
  handshake", "send call", "wait for vendor" and "receive reply". On the
  first call of an app's TLS connection, "app tls handshake" comes first,
  inside the call span. A step that did not happen has no span.

  Nothing is on both the trace and the record, unless there is a good
  reason (Peter, 2026-10-31). The trace id and span id are on both, to
  link them, and the record keeps hotline.timing.total_us, the call span's
  length. Spans hold no method, route, status, host or address: those are
  on the record. Times come from the proxy's clock.

  Rule: The steps of a call

    Scenario: A call on a new https connection
      Given a vendor reached over a new https connection
      When a call ends
      Then the trace has the spans "hotline call", "routing", "vendor call", "dns lookup", "tcp connect", "vendor tls handshake", "send call", "wait for vendor", "receive reply", "reply to app", "request spec check", "reply spec check", "wait for check worker" and "wait for check worker"
      And "routing", "vendor call" and "reply to app" are under "hotline call", one after another
      And "dns lookup", "tcp connect", "vendor tls handshake", "send call", "wait for vendor" and "receive reply" are under "vendor call", one after another
      And every span but the spec checks is within its parent
      And "request spec check" starts when "send call" ends, under "hotline call"
      And "reply spec check" starts when "receive reply" ends, under "hotline call"
      And "hotline call" is a server span, "vendor call" a client span, and the others internal
      And "dns lookup" has dns.question.name api.talon.one
      And "vendor call" has hotline.connection.reused false
      And no other span has an attribute

    Scenario: The record keeps only the total
      Given a vendor reached over a new https connection
      When a call ends
      Then the record's hotline.timing.total_us is the length of "hotline call"
      And the record has no other hotline.timing attribute
      And the record has no hotline.connection.reused or dns.question.name

    Scenario: The call span ends with the last byte to the app
      Given a vendor reached over a new https connection
      When the last byte of the reply has gone to the app
      Then "hotline call" ends at that moment
      And "receive reply" lasts at least as long as the vendor took to send the rest

    Scenario: Plain http has no vendor TLS step
      Given a vendor reached over plain http
      When a call ends
      Then the trace has no "vendor tls handshake" span

    Scenario: An IP address needs no lookup
      Given a vendor whose host is an IP address
      When a call ends
      Then the trace has no "dns lookup" span

    Scenario: A reused connection
      Given a connection to the vendor that is already open
      When a call reuses it
      Then the trace has no "dns lookup", "tcp connect" or "vendor tls handshake" span
      And "vendor call" has hotline.connection.reused true

    Scenario: A call that gets a freed connection while its own is still being opened
      The connection being opened goes on in the background. Its DNS and
      connect events belong to no call, so they must not change this one.
      Given a call that waits for a new connection while another call holds the only open one
      When the other call ends first and frees its connection
      Then the waiting call's trace says hotline.connection.reused true, with no "dns lookup", "tcp connect" or "vendor tls handshake" span

    Scenario: Several addresses are tried one after another
      Given a vendor whose IPv6 address cannot be reached and whose IPv4 address can
      When a call ends
      Then the call goes to the IPv4 address
      And there is one "tcp connect" span, from the first try until connected

    Scenario: An address that never answers gets only its share of the wait
      The wait is shared between the addresses still to try, as Go's own
      dialer does, so one dead address can't use up all of vendorTimeout.
      Given a vendor whose first address never answers, with a vendorTimeout of 10 seconds
      When a call ends
      Then the call goes to its second address
      And "tcp connect" lasts just over 5 seconds, the first address's share of the wait

  Rule: A failed step

    Scenario Outline: The step that failed ends there, marked as an error
      Given a call where <failure>
      When the call ends
      Then the "<step>" span ends with status error
      And "vendor call" and "hotline call" end with status error
      And the trace has no "<missing>" span
      And no span has an error.type

      Examples:
        | failure                  | step                 | missing       |
        | DNS fails                | dns lookup           | tcp connect   |
        | the TLS handshake fails  | vendor tls handshake | send call     |
        | the vendor never answers | wait for vendor      | receive reply |
        | the app's body breaks off | send call           | wait for vendor |

    Scenario Outline: The app hangs up before it has the whole reply
      The vendor did nothing wrong, so "vendor call" is not marked. The
      record's hotline.client.cancelled says why (Peter, 2026-10-31).
      Given <vendor>
      When the app disconnects after 100 milliseconds
      Then the "reply to app" span ends with status error
      And the "hotline call" span ends with status error
      And the "vendor call" span ends with status unset
      And the record says hotline.client.cancelled true

      Examples:
        | vendor                                                                  |
        | a vendor that answers after 300 milliseconds and a vendorTimeout of 10 seconds |
        | a vendor that sends half of its reply, and the rest 300 milliseconds later |

    Scenario: A refused call has only the call span
      When an app sends "GET /v2/x" with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then the trace has only the span "hotline call", with status error

  Rule: The trace and the record are linked

    Scenario: The call span has the record's ids
      When a call ends
      Then "hotline call" has the record's trace_id and span_id
      And every span has the record's trace_id

    Scenario: The proxy's spans join the app's trace
      When a call ends with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then "hotline call" has the trace_id 4bf92f3577b34da6a3ce929d0e0e4736 and the parent 00f067aa0ba902b7

    Scenario: Without the app's trace context, the call span has no parent
      When a call ends
      Then "hotline call" has no parent

    Scenario: Only the app's span is a remote parent
      A span's flags say whether its parent ran in another program, which
      OpenTelemetry calls remote. Only the parent of "hotline call", the
      app's own span, did; every other span's parent is a step of the proxy
      itself (Peter, 2026-11-08).
      When a call ends with "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
      Then "hotline call" says its parent is remote
      And every other span says its parent is not remote

    Scenario: A call span with no parent says so
      When a call ends
      Then "hotline call" says its parent is not remote

    Scenario: A call without an integration gets a trace too
      Given the app sends no integration ID
      When a call ends
      Then the trace has the spans "hotline call", "routing", "vendor call", "dns lookup", "tcp connect", "vendor tls handshake", "send call", "wait for vendor", "receive reply" and "reply to app"

  Rule: Spans are sent like records

    Scenario: Spans go to /v1/traces
      When a call ends
      Then the receiver gets the spans at /v1/traces, with the exporter's resource labels

    Scenario: Every exporter gets the trace
      Given a second exporter
      When a call ends
      Then both receivers get the same spans
