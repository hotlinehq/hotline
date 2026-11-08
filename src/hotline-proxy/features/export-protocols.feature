Feature: How OpenTelemetry data is sent
  Each exporter picks gRPC, the default, or HTTP. Over HTTP the proxy sends
  protobuf (a compact binary format) to /v1/logs, /v1/metrics and
  /v1/traces. Both
  protocols take the same login headers, TLS settings and compression, and
  only the config file says how: the standard OTEL_EXPORTER_OTLP_*
  environment variables are ignored (Peter, 2026-10-30). Tests use a fake
  OTLP receiver with a gRPC and an HTTP side.

  Scenario: gRPC stays the default
    Given an exporter with no protocol set
    When a call ends
    Then the receiver gets the record and the count over gRPC

  Scenario: Records and counts over HTTP
    Given an HTTP receiver
    When a call ends
    Then the receiver gets the record on /v1/logs and the count on /v1/metrics
    And every request is sent as application/x-protobuf

  Scenario: A path in the endpoint is kept
    Given an HTTP receiver whose endpoint ends in /otlp
    When a call ends
    Then the receiver gets the record on /otlp/v1/logs

  Scenario: Exporters of both protocols get the same data
    Given a gRPC receiver and an HTTP receiver
    When a call ends
    Then both get the same record and count

  Scenario Outline: TLS to the collector
    An https:// endpoint means TLS, for gRPC too. The collector's
    certificate is checked against the system's issuers, the trust file
    and tls.ca.
    Given a <protocol> receiver over TLS whose issuer only tls.ca trusts
    When a call ends
    Then the receiver gets the record over TLS

    Examples:
      | protocol |
      | gRPC     |
      | HTTP     |

  Scenario Outline: An untrusted collector gets nothing
    Given a <protocol> receiver over TLS whose issuer nobody trusts
    When a call ends
    Then the receiver gets no record
    And the proxy logs a warning that the send to that endpoint failed

    Examples:
      | protocol |
      | gRPC     |
      | HTTP     |

  Scenario Outline: Mutual TLS
    In mutual TLS the proxy also shows a certificate of its own, from
    tls.cert and tls.key.
    Given a <protocol> receiver over TLS that asks for a client certificate
    When a call ends
    Then the receiver saw the proxy's client certificate

    Examples:
      | protocol |
      | gRPC     |
      | HTTP     |

  Scenario Outline: Login headers
    A secret value comes from a file or an environment variable, never
    from the config file itself.
    Given a <protocol> receiver
    And the exporter sends the header Authorization from <source>
    When a call ends
    Then every request to the receiver carried that Authorization value
    And the value is in none of the proxy's log lines

    Examples:
      | protocol | source                  |
      | gRPC     | a plain value           |
      | gRPC     | a file                  |
      | HTTP     | a file                  |
      | HTTP     | an environment variable |

  Scenario Outline: Compression
    Given a <protocol> receiver and an exporter that <sets>
    When a call ends
    Then every request to the receiver is <encoding>

    Examples:
      | protocol | sets                     | encoding |
      | gRPC     | does not set compression | gzipped  |
      | gRPC     | sets compression to none | plain    |
      | HTTP     | does not set compression | gzipped  |
      | HTTP     | sets compression to none | plain    |

  Scenario Outline: Retries over HTTP
    Busy answers are tried again within the send's time limit. Any other
    refusal loses the batch, and the loss is logged like a full queue.
    Given an HTTP receiver that answers the first log send with <answer>
    When a call ends
    Then the record <arrives>

    Examples:
      | answer                 | arrives                                        |
      | 503                    | arrives on the second try                      |
      | 429 with Retry-After 1 | arrives on the second try                      |
      | 400                    | is lost, and the proxy logs that 1 was dropped |

  Scenario Outline: Retries over gRPC
    The same rule as over HTTP: a busy collector is tried again, any other
    refusal loses the batch.
    Given a gRPC receiver that answers the first log send with <code>
    When a call ends
    Then the record <arrives>

    Examples:
      | code             | arrives                                        |
      | Unavailable      | arrives on the second try                      |
      | InvalidArgument  | is lost, and the proxy logs that 1 was dropped |

  Scenario Outline: HTTP/2 to the collector
    Over TLS the proxy uses HTTP/2 when the collector offers it, and
    HTTP/1.1 when it does not. A plain http:// endpoint uses HTTP/1.1,
    unless h2c is true: then it speaks HTTP/2 from the first byte, which is
    called h2c, HTTP/2 without TLS (Peter, 2026-11-08).
    Given an HTTP receiver <over> that offers <offers>
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
    Given an HTTP receiver plain that offers HTTP/1.1
    And the exporter sets h2c to true
    When a call ends
    Then the receiver gets no record
    And the proxy logs a warning that the send to that endpoint failed

  Scenario: The standard environment variables are ignored
    Given an HTTP receiver
    And OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_HEADERS and OTEL_EXPORTER_OTLP_COMPRESSION are set to other values
    When a call ends
    Then the receiver gets the record, gzipped, with no header from the environment
