Feature: Probing a vendor the proxy cannot connect to
  When calls to a vendor fail to connect, the proxy probes the vendor's
  endpoint (its host and port) off the call path, and sends one log record
  and one trace that say which side of the connection the problem is on.
  Many integrations calling the same endpoint share one probe, and probes
  back off while the failures go on (Peter, 2026-11-21). Every call record
  carries proof of the latest probe of its endpoint while its failures go
  on (Peter, 2026-11-22). When the vendor's port gives no answer or cannot
  be reached, the probe also traces the path to the vendor, router by
  router, and asks whether the vendor's machine is up (slice 2, Peter,
  2026-11-22).

  # Starting a probe

  Scenario Outline: A failed connection starts a probe
    Given <situation>
    When an app calls the vendor through the proxy
    Then one probe of "<endpoint>" is exported

    Examples:
      | situation                                                          | endpoint                   |
      | a vendor host that does not exist                                  | no-such-host.talon.one:443 |
      | a DNS lookup that takes too long                                   | api.talon.one:443          |
      | a vendor that refuses the connection                               | api.talon.one:443          |
      | a vendor that takes too long to connect                            | api.talon.one:443          |
      | a vendor whose network cannot be reached                           | api.talon.one:443          |
      | a TLS handshake that fails for a reason other than the certificate | api.talon.one:443          |
      | a vendor with a self-signed certificate                            | api.talon.one:443          |

  Scenario Outline: Failures after the connection worked start no probe
    Given <situation>
    When an app calls the vendor through the proxy
    Then no probe is exported

    Examples:
      | situation                                            |
      | a vendor slower than vendorTimeout                   |
      | a vendor that closes the connection before answering |
      | a vendor host that resolves only to internal IPs     |
      | a vendor that answers 500                            |

  Scenario: A probe makes no call record of its own
    Given the vendor refuses connections
    When an app calls the vendor through the proxy
    Then one probe of "api.talon.one:443" is exported
    And the proxy exports exactly one call record

  # One probe per endpoint, with backoff

  Scenario: Many failed calls to one endpoint make one probe
    Given the vendor refuses connections
    When 20 calls from three integrations fail within one second
    Then one probe of "api.talon.one:443" is exported

  Scenario: Two endpoints are probed apart
    Given the vendor refuses connections on ports 443 and 8443
    When an app calls each port through the proxy
    Then one probe of "api.talon.one:443" is exported
    And one probe of "api.talon.one:8443" is exported

  Scenario: The probe backs off while calls keep failing
    Given the vendor refuses connections
    When a call to it fails every minute for 40 minutes
    Then probes of "api.talon.one:443" start at minutes 0, 1, 3, 7, 15 and 30

  Scenario: A successful call ends the backoff
    Given the vendor refuses connections
    And a call to it fails every minute for 4 minutes
    When the vendor accepts connections again and a call succeeds
    And a call to it fails again 1 minute later
    Then probes of "api.talon.one:443" start at minutes 0, 1, 3 and 5

  Scenario: A quiet spell ends the backoff
    Given the vendor refuses connections
    And a call to it fails every minute for 4 minutes
    When no call to it fails for 15 minutes
    And a call to it fails after that
    Then probes of "api.talon.one:443" start at minutes 0, 1, 3, 7 and 19

  Scenario: The backoff of an api's own probe settings
    Given the api for "api.talon.one" has "probe: { backoff: { first: 5m, longest: 20m } }"
    And the vendor refuses connections
    When a call to it fails every minute for 60 minutes
    Then probes of "api.talon.one:443" start at minutes 0, 5, 15, 35 and 55

  Scenario: An api can turn the probe off
    Given the api for "api.talon.one" has "probe: { enabled: false }"
    And the vendor refuses connections
    When an app calls the vendor through the proxy
    Then no probe is exported

  Scenario: Two apis on one endpoint: the longer wait wins
    Given two apis for "api.talon.one", one with "probe: { backoff: { first: 5m } }" and one with no probe settings
    And the vendor refuses connections
    When a call through each api fails every minute for 10 minutes
    Then probes of "api.talon.one:443" start at minutes 0 and 5

  Scenario: Two apis on one endpoint: off wins
    Given two apis for "api.talon.one", one with "probe: { enabled: false }" and one with no probe settings
    And the vendor refuses connections
    When a call through each api fails every minute for 2 minutes
    Then no probe is exported

  Scenario: Calls of no known integration are probed too
    Given the vendor refuses connections
    When an app with no integration ID calls the vendor through the proxy
    Then one probe of "api.talon.one:443" is exported

  # What a probe finds

  Scenario Outline: The side and the verdict
    Given <situation>
    When an app calls the vendor through the proxy
    Then the probe's record has hotline.probe.side <side> and hotline.probe.verdict <verdict>
    And its answers are name_resolves <name>, port_accepts_us <port> and tls_works <tls>

    Examples:
      | situation                                                          | side        | verdict                | name | port        | tls         |
      | a vendor host that does not exist                                  | our_side    | dns_name_not_found     | no   | not_checked | not_checked |
      | a DNS lookup that takes too long                                   | our_side    | dns_failed             | no   | not_checked | not_checked |
      | a vendor that refuses the connection                               | vendor_side | refused_by_vendor      | yes  | no          | not_checked |
      | a vendor that takes too long to connect                            | between     | no_answer              | yes  | no          | not_checked |
      | a vendor whose network cannot be reached                           | between     | unreachable            | yes  | no          | not_checked |
      | a vendor that cuts the connection as TLS starts                    | vendor_side | tls_cut_by_vendor      | yes  | yes         | no          |
      | a vendor with a self-signed certificate                            | vendor_side | tls_certificate_failed | yes  | yes         | no          |
      | a TLS handshake that fails for a reason other than the certificate | vendor_side | tls_failed             | yes  | yes         | no          |
      | a vendor that refused one call and now accepts connections         | none        | reachable_now          | yes  | yes         | yes         |
      | a vendor that never answers the TLS handshake                      | vendor_side | tls_failed             | yes  | yes         | no          |
      | a vendor that refused one call and whose name now resolves only to an internal IP | our_side | dns_failed | no | not_checked | not_checked |
      | a vendor called by its IP address that refuses the connection      | vendor_side | refused_by_vendor      | yes  | no          | not_checked |

  Scenario: A vendor called over plain HTTP has no TLS to check
    Given a vendor that refused one call over plain HTTP and now accepts connections
    When an app calls the vendor through the proxy
    Then the probe's record has hotline.probe.side none and hotline.probe.verdict reachable_now
    And its answers are name_resolves yes, port_accepts_us yes and tls_works not_checked

  Scenario: The probe keeps to the internal address guard
    Given a vendor host whose name resolves to a private IP and to a public IP that refuses the connection
    When an app calls the vendor through the proxy
    Then the probe's record has hotline.probe.side vendor_side and hotline.probe.verdict refused_by_vendor
    And the proxy never tried to connect to the private IP

  Scenario: A stop that runs out of time drops the probe still running
    What a cut probe found says nothing about the vendor.

    Given a vendor that refused one call and then never answers, while no time passes
    When an app calls the vendor through the proxy
    And its probe is running
    And the proxy is stopped with no time left
    Then no probe is exported

  # Where the path stops (slice 2)

  Scenario Outline: The path trace tells where a silent vendor's packets stop
    Given <situation>
    When an app calls the vendor through the proxy
    Then the probe's record has hotline.probe.side <side> and hotline.probe.verdict <verdict>
    And its path answers are path_reaches_vendor <path> and host_up <host>
    And the probe's body says "<says>"

    Examples:
      | situation                                                                      | side        | verdict                | path        | host        | says                          |
      | a vendor whose firewall drops our packets, and whose machine answers UDP       | vendor_side | silent_drop_at_vendor  | yes         | yes         | most likely drops our IP      |
      | a vendor whose firewall drops our packets, and whose machine answers only ping | vendor_side | silent_drop_at_vendor  | yes         | yes         | most likely drops our IP      |
      | a vendor whose firewall drops our packets, where ping is not allowed           | between     | silent_drop            | no          | no          | most likely                   |
      | a path whose packets stop after our own router                                 | our_side    | path_stops_on_our_side | no          | no          | after router 1 (10.0.0.1)     |
      | a path whose packets stop after a public router                                | between     | silent_drop            | no          | no          | after router 2 (198.51.100.2) |
      | a path where no router answers                                                 | between     | silent_drop            | no          | no          | quiet from the first router   |
      | a firewall on the way that answers "administratively prohibited"               | vendor_side | rejected_by_firewall   | yes         | yes         | 198.51.100.2                  |
      | a firewall of ours on the way that answers "administratively prohibited"       | our_side    | rejected_by_firewall   | no          | no          | 10.0.0.1                      |
      | a router on the way that says the vendor's machine cannot be reached           | between     | unreachable            | no          | no          | 198.51.100.2 reports          |
      | a vendor whose firewall drops our packets, where path probes cannot run        | between     | no_answer              | not_checked | not_checked | did not answer                |

  Scenario: Each router is a span of the path's trace
    Given a vendor whose firewall drops our packets, and whose machine answers UDP
    When an app calls the vendor through the proxy
    Then the probe's trace has spans "name_resolves", "path_reaches_vendor", "host_up" and "port_accepts_us", in that order
    And the span "path_reaches_vendor" has "hop" spans under it for tcp with hotline.probe.hop.ttl 1 to 6, and for udp 1 to 4
    And the first hop span has hotline.probe.hop.from "203.0.113.1", hotline.probe.hop.answer "time_exceeded" and network.transport "tcp"
    And the probe's hotline.probe.path_reaches_vendor.evidence has a line "tcp ttl 1: time exceeded from 203.0.113.1"

  Scenario: The trace gives up after three silent routers in a row
    Given a path where no router answers
    When an app calls the vendor through the proxy
    Then the span "path_reaches_vendor" has "hop" spans under it for tcp with hotline.probe.hop.ttl 1 to 3, and for udp 1 to 3

  Scenario: A refusal sends no path packets
    Given the vendor refuses connections
    When an app calls the vendor through the proxy
    Then one probe of "api.talon.one:443" is exported
    And the proxy sent no path packets
    And its path answers are path_reaches_vendor not_checked and host_up not_checked

  Scenario: The path trace goes no further than maxHops
    Given the api for "api.talon.one" has "probe: { maxHops: 2 }"
    And a vendor whose firewall drops our packets, and whose machine answers UDP
    When an app calls the vendor through the proxy
    Then no TCP or UDP path packet crossed more than 2 routers

  # What is exported

  Scenario: The probe's log record
    Given the vendor refuses connections
    When an app calls the vendor through the proxy
    Then the probe's log record has event name "hotline.probe" and severity WARN
    And its body names "api.talon.one:443" and says it refused the connection
    And it has server.address "api.talon.one" and server.port 443
    And its hotline.probe.port_accepts_us.evidence names the vendor's IP and a reset
    And it carries the trace id and span id of the probe's root span
    And it has a hotline.record.hash in the ledger chain "hotline.probe"

  Scenario: A passing blip is logged at info
    Given a vendor that refused one call and now accepts connections
    When an app calls the vendor through the proxy
    Then the probe's log record has severity INFO and hotline.probe.verdict reachable_now

  Scenario: The probe's trace
    Given the vendor refuses connections
    When an app calls the vendor through the proxy
    Then the probe's trace has a root span "probe" with hotline.probe.side vendor_side
    And the root span links to the call span of the failed call
    And under it are spans "name_resolves" and "port_accepts_us", in that order
    And each has hotline.probe.question, hotline.probe.answer, hotline.probe.points_to and hotline.probe.evidence

  Scenario: A probe made due by a later failure links to that failure
    Given the vendor refuses connections
    When a call to it fails every minute for 2 minutes
    Then the second probe's root span links to the call span of the last call that failed before it

  Scenario: Later failed calls carry the latest probe
    Given the vendor refuses connections
    When a call to it fails every minute for 2 minutes
    Then the second call's record has hotline.probe.side vendor_side and hotline.probe.verdict refused_by_vendor
    And it has hotline.probe.record_hash and hotline.probe.trace_id of the first probe
    And it has hotline.probe.time, when that probe ended

  Scenario: A successful call while the schedule runs links to the latest probe
    Given the vendor refuses connections
    And a call to it fails every minute for 1 minutes
    When the vendor accepts connections again and a call succeeds
    Then that call's record has hotline.probe.record_hash and hotline.probe.trace_id of the first probe
    And it has no hotline.probe.side and no hotline.probe.verdict

  Scenario: The call that starts the first probe carries no probe link
    Given the vendor refuses connections
    When an app calls the vendor through the proxy
    Then the first call's record has no hotline.probe attributes

  Scenario: Calls carry no probe link once the vendor has recovered
    Given the vendor refuses connections
    And a call to it fails every minute for 1 minutes
    When the vendor accepts connections again and a call succeeds
    And another call to it succeeds
    Then the last call's record has no hotline.probe attributes

  Scenario: Calls to a vendor that never failed carry no probe link
    Given a vendor that answers normally
    When an app calls the vendor through the proxy
    Then the first call's record has no hotline.probe attributes
    And no probe is exported

  Scenario: The file exporter gets the probe
    Given a file exporter
    And the vendor refuses connections
    When an app calls the vendor through the proxy
    Then the file has a line with event "hotline.probe", its verdict sentence as body, and its root span's link

  Scenario: The console line of a probe
    Given the vendor refuses connections
    And a console exporter
    When an app calls the vendor through the proxy
    Then the console prints a line with "PROBE", "api.talon.one:443", "vendor_side" and "refused the connection"

  Scenario: The console line of a passing blip is green
    Given a vendor that refused one call and now accepts connections
    And the console exporter has color always
    When an app calls the vendor through the proxy
    Then the console prints the probe's line in green

  Scenario: hotline-proxy verify accepts the probes' chain
    Given a file exporter
    And the vendor refuses connections
    When an app calls the vendor through the proxy
    Then hotline-proxy verify finds every record and batch of the file sound

  # Config

  Scenario Outline: A bad probe setting makes the config invalid
    Given the api for "api.talon.one" has "<probe>"
    When the proxy starts with it
    Then the proxy refuses the config: <error>

    Examples:
      | probe                                           | error                                                                         |
      | probe: { backoff: { first: 0s } }               | api talon-one: probe: backoff.first "0s" must be a duration above zero        |
      | probe: { backoff: { first: 20m, longest: 5m } } | api talon-one: probe: backoff.first 20m0s is longer than backoff.longest 5m0s |
      | probe: { probeTimeout: -1s }                    | api talon-one: probe: probeTimeout "-1s" must be a duration above zero        |
      | probe: { maxHops: 0 }                           | api talon-one: probe: maxHops 0 must be from 1 to 64                          |
      | probe: { maxHops: 65 }                          | api talon-one: probe: maxHops 65 must be from 1 to 64                         |

  Scenario: An unknown probe setting is a warning
    Given the api for "api.talon.one" has "probe: { colour: red }"
    When the proxy starts with it
    Then the proxy logged a warning with "field colour" and "is not a setting and is ignored"

  Scenario: An integration has no probe settings
    Given the integration talon-shop-a has "probe: { enabled: false }"
    When the proxy starts with it
    Then the proxy logged a warning with "field probe" and "is not a setting and is ignored"

  Scenario: A reload applies new settings from the next wait
    Given the vendor refuses connections
    And a call to it fails every minute for 8 minutes
    When the config is reloaded with "probe: { backoff: { first: 1m, longest: 2m } }" on the api
    And a call to it fails every minute for 12 minutes more
    Then probes of "api.talon.one:443" start at minutes 0, 1, 3, 7, 15, 17 and 19

  Scenario: A reload that turns the probe off stops it
    Given the vendor refuses connections
    And a call to it fails every minute for 2 minutes
    When the config is reloaded with "probe: { enabled: false }" on the api
    And a call to it fails every minute for 10 minutes more
    Then probes of "api.talon.one:443" start at minutes 0 and 1
