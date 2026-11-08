Feature: How OpenTelemetry data is sent
  The proxy sends OTLP over HTTP as protobuf (a compact binary format) to
  /v1/logs, /v1/metrics and /v1/traces after the endpoint's own path. gRPC
  is not offered: its port usually takes nothing else, and HTTP goes
  through the load balancers, proxies and firewalls companies already
  have (Peter, 2026-11-08). Only the config file says how data is sent:
  the standard OTEL_EXPORTER_OTLP_* environment variables are ignored
  (Peter, 2026-10-30). Tests use a fake OTLP receiver over HTTP.

  Scenario: Records, counts and spans over HTTP
    Given a receiver
    When a call ends
    Then the receiver gets the record on /v1/logs and the count on /v1/metrics
    And every request is sent as application/x-protobuf

  Scenario: A path in the endpoint is kept
    Given a receiver whose endpoint ends in /otlp
    When a call ends
    Then the receiver gets the record on /otlp/v1/logs

  Scenario: TLS to the collector
    An https:// endpoint means TLS. The collector's certificate is checked
    against the system's issuers, the trust file and tls.ca.
    Given a receiver over TLS whose issuer only tls.ca trusts
    When a call ends
    Then the receiver gets the record over TLS

  Scenario: An untrusted collector gets nothing
    Given a receiver over TLS whose issuer nobody trusts
    When a call ends
    Then the receiver gets no record
    And the proxy logs a warning that the send to that endpoint failed

  Scenario: Mutual TLS
    In mutual TLS the proxy also shows a certificate of its own, from
    tls.cert and tls.key.
    Given a receiver over TLS that asks for a client certificate
    When a call ends
    Then the receiver saw the proxy's client certificate

  Scenario Outline: Login headers
    A secret value comes from a file or an environment variable, never
    from the config file itself.
    Given a receiver
    And the exporter sends the header Authorization from <source>
    When a call ends
    Then every request to the receiver carried that Authorization value
    And the value is in none of the proxy's log lines

    Examples:
      | source                  |
      | a plain value           |
      | a file                  |
      | an environment variable |

  Scenario Outline: Compression
    Given a receiver and an exporter that <sets>
    When a call ends
    Then every request to the receiver is <encoding>

    Examples:
      | sets                     | encoding |
      | does not set compression | gzipped  |
      | sets compression to none | plain    |

  Scenario Outline: Retries
    Busy answers are tried again within the send's time limit. Any other
    refusal loses the batch, and the loss is logged like a full queue.
    Given a receiver that answers the first log send with <answer>
    When a call ends
    Then the record <arrives>

    Examples:
      | answer                 | arrives                                        |
      | 503                    | arrives on the second try                      |
      | 429 with Retry-After 1 | arrives on the second try                      |
      | 400                    | is lost, and the proxy logs that 1 was dropped |

  Scenario Outline: HTTP/2 to the collector
    Over TLS the proxy uses HTTP/2 when the collector offers it, and
    HTTP/1.1 when it does not. A plain http:// endpoint uses HTTP/1.1,
    unless h2c is true: then it speaks HTTP/2 from the first byte, which is
    called h2c, HTTP/2 without TLS (Peter, 2026-11-08).
    Given a receiver <over> that offers <offers>
    And the exporter <sets>
    When a call ends
    Then the receiver gets the record
    And every request to the receiver came over <version>

    Examples:
      | over     | offers      | sets             | version  |
      | over TLS | HTTP/2      | does not set h2c | HTTP/2.0 |
      | over TLS | HTTP/1.1    | does not set h2c | HTTP/1.1 |
      | plain    | h2c         | does not set h2c | HTTP/1.1 |
      | plain    | h2c         | sets h2c to true | HTTP/2.0 |

  Scenario: A collector without h2c gets nothing
    Given a receiver plain that offers HTTP/1.1
    And the exporter sets h2c to true
    When a call ends
    Then the receiver gets no record
    And the proxy logs a warning that the send to that endpoint failed

  Scenario: The standard environment variables are ignored
    Given a receiver
    And OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS and OTEL_EXPORTER_OTLP_COMPRESSION are set to other values
    When a call ends
    Then the receiver gets the record, gzipped, with no header from the environment
