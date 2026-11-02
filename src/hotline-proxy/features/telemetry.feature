Feature: The proxy's own telemetry
  The proxy sends numbers about itself as OpenTelemetry metrics to the
  telemetry destination only, never mixed with the records of vendor calls:
  calls in flight, how long calls take, failed TLS setups with apps, each
  exporter, config reloads, certificates and the Go runtime: 14 metrics
  (Peter, 2026-11-01). Standard names where OpenTelemetry has one,
  hotline. for our own. Every metric has a description and a unit that
  Grafana can chart; a 1 or 0 gauge has no unit, so its Prometheus name
  gets no _ratio ending. No number is
  split by integration, vendor, host, URL or route, so a proxy with a
  million integrations sends as few numbers as one with one (Peter,
  2026-10-31). Every metric can be turned off by name.

  Background:
    Given a fake OTLP receiver for the proxy's own telemetry

  Scenario: Own data goes only to the telemetry destination
    When a call ends
    Then the telemetry receiver gets the metric http.server.request.duration
    And the telemetry receiver gets no log records
    And the product receiver gets no metric but hotline.requests

  Scenario: Calls in flight, by ingress only
    Given a vendor that keeps the call waiting
    When an app starts a forward proxy GET call over http
    Then http.server.active_requests is 1 for hotline.ingress forward_proxy
    And it has no label but hotline.ingress
    And once the call ends it is 0

  Scenario: The time each call takes, as the standard measures it
    When an app makes a forward proxy GET call over http and the vendor answers 200
    Then the telemetry receiver gets the histogram http.server.request.duration with unit s
    And its range limits are 0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5 and 10
    And it counts 1 call for http.response.status_code 200 and hotline.ingress forward_proxy
    And it has no label but http.response.status_code, error.type and hotline.ingress

  Scenario: A call the proxy fails names its error type
    Given the vendor's name does not resolve
    When an app makes a forward proxy GET call over http
    Then http.server.request.duration counts 1 call with error.type dns_error

  Scenario: The proxy's own time per call is not a metric
    Records and traces already carry it for every call (Peter, 2026-11-01).
    When an app makes a forward proxy GET call over http and the vendor answers 200
    Then the telemetry receiver gets the histogram http.server.request.duration
    And the telemetry receiver gets no metric hotline.proxy.overhead

  Scenario: The proxy's own numbers go out once a minute
    As with counts, unless telemetry.otlp.metricInterval says otherwise
    (Peter, 2026-11-01). The telemetry destination gets only metrics, so a
    logInterval or traceInterval there is ignored with a warning.
    Given the telemetry destination does not set metricInterval
    When a call ends
    Then the telemetry receiver gets no metric within a second
    And the telemetry receiver gets the metric http.server.request.duration once the proxy stops

  Scenario Outline: Log and trace intervals mean nothing for telemetry
    Given the telemetry destination sets <setting>
    Then the proxy logs a warning naming telemetry.otlp.<setting>

    Examples:
      | setting       |
      | logInterval   |
      | traceInterval |

  Scenario: The queue of each exporter
    When a call ends
    Then hotline.exporter.queue.size is sent for that exporter and hotline.signal record
    And hotline.exporter.queue.size is sent for that exporter and hotline.signal span
    And the telemetry receiver gets no metric starting with otel.sdk.

  Scenario: Records sent fine are counted
    When 3 calls end
    Then hotline.exporter.sent counts 3 for that exporter and hotline.signal record

  Scenario: Spans sent fine are counted
    Given a vendor reached over a new https connection
    When a call ends
    Then hotline.exporter.sent counts 14 for that exporter and hotline.signal span

  Scenario: Counts sent to an exporter are counted too
    When a call ends
    Then hotline.exporter.sent counts some for that exporter and hotline.signal count

  Scenario: Records dropped from a full queue are counted
    Given an exporter with queueSize 3 whose destination is busy with the first record
    When 4 more calls end
    Then hotline.exporter.dropped counts 1 for that exporter, hotline.signal record and hotline.drop.reason queue_full

  Scenario: Records lost in a refused send are counted
    Given a product destination over HTTP that refuses every log export with 400
    When a call ends
    Then hotline.exporter.dropped counts 1 for that exporter, hotline.signal record and hotline.drop.reason send_failed

  Scenario: An exporter that works shows as up
    When a call ends
    Then hotline.exporter.up is 1 for that exporter

  Scenario: An exporter that is down shows as down, and up again once it works
    Given a product destination that is down
    When a call ends
    Then hotline.exporter.up turns 0 for that exporter
    When the destination comes back and another call ends
    Then hotline.exporter.up turns 1 for that exporter

  Scenario: The config at start
    When the proxy starts
    Then hotline.config.reload_failed is 0
    And hotline.config.restart_needed is 0
    And the telemetry receiver gets no metric hotline.config.info
    And the telemetry receiver gets no metric hotline.config.reloads

  Scenario: A failed reload stays visible
    Given the proxy runs
    When the config file changes so that it is not valid YAML
    And the proxy checks its files
    Then hotline.config.reload_failed turns 1

  Scenario: A good reload after a failed one clears the failure
    Given the proxy runs
    And the config file changes so that it is not valid YAML
    And the proxy checks its files
    When the config file changes so that talon-shop-b also uses Talon.One
    And the proxy checks its files
    Then hotline.config.reload_failed turns 0

  Scenario: A change that needs a restart shows until the restart
    Given the proxy runs
    When the config file changes so that listen.proxy moves to another port
    And the proxy checks its files
    Then hotline.config.restart_needed turns 1

  Scenario: A change put back no longer needs a restart
    Given the proxy runs
    And the config file changes so that listen.proxy moves to another port
    And the proxy checks its files
    When the config file is put back as it was
    And the proxy checks its files
    Then hotline.config.restart_needed turns 0

  Scenario: The proxy's own certificate
    The expiry is a time in seconds since 1970, so an alert reads "expires
    within 7 days" and the number does not change every day.
    Given the proxy port takes TLS with a certificate that expires in 90 days
    When the proxy starts
    Then hotline.certificate.not_after is that certificate's expiry for hotline.certificate.use listen.tls

  Scenario: A renewed certificate shows its new date
    Given the proxy port takes TLS with a certificate that expires in 90 days
    And the proxy runs
    When the cert and key files are replaced with a certificate that expires in 120 days
    And the proxy checks its files
    Then hotline.certificate.not_after turns to the new certificate's expiry for hotline.certificate.use listen.tls

  Scenario: The CONNECT issuer
    Given CONNECT is an ingress with an issuer that expires in 200 days
    When the proxy starts
    Then hotline.certificate.not_after is that issuer's expiry for hotline.certificate.use listen.connect

  Scenario Outline: Failed TLS setups with apps are counted
    Given the proxy port takes TLS
    When an app <does>
    Then hotline.app_tls.failures counts 1 with error.type <type> and hotline.ingress <ingress>

    Examples:
      | does                                             | type                 | ingress   |
      | that does not trust the proxy opens TLS          | tls_untrusted_by_app | none      |
      | sends plain HTTP to the TLS port                 | plain_http           | none      |
      | that allows only TLS 1.1 opens TLS               | tls_version          | none      |
      | that does not trust the issuer opens a tunnel    | tls_untrusted_by_app | connect   |

  Scenario: The Go runtime, five numbers
    go.memory.limit is the fifth, sent only when a memory limit is set
    with GOMEMLIMIT. go.memory.allocated, the memory handed out since the
    start, is there for the load test (Peter, 2026-11-02).
    When the proxy starts
    Then the telemetry receiver gets the metrics go.memory.used and go.goroutine.count
    And the telemetry receiver gets the metric go.memory.gc.goal
    And the telemetry receiver gets the metric go.memory.allocated
    And the telemetry receiver gets no metric go.memory.allocations
    And the telemetry receiver gets no metric go.processor.limit
    And the telemetry receiver gets no metric go.config.gogc

  Scenario Outline: Each metric has its unit and a description
    A 1 or 0 gauge has no unit, so Prometheus adds no _ratio to its name
    (Peter, 2026-11-01).
    Given the proxy port takes TLS
    When a call ends
    Then the telemetry receiver gets <name> with unit "<unit>" and a description

    Examples:
      | name                          | unit         |
      | http.server.active_requests   | {request}    |
      | http.server.request.duration  | s            |
      | hotline.exporter.up           |              |
      | hotline.exporter.queue.size   | {item}       |
      | hotline.exporter.sent         | {item}       |
      | hotline.config.reload_failed  |              |
      | hotline.config.restart_needed |              |
      | hotline.certificate.not_after | s            |
      | go.memory.used                | By           |
      | go.goroutine.count            | {goroutine}  |
      | go.memory.allocated           | By           |

  Scenario: Failed TLS setups have their unit and a description
    Given the proxy port takes TLS
    When an app sends plain HTTP to the TLS port
    Then the telemetry receiver gets hotline.app_tls.failures with unit "{connection}" and a description

  Scenario: Dropped items have their unit and a description
    Given an exporter with queueSize 3 whose destination is busy with the first record
    When 4 more calls end
    Then the telemetry receiver gets hotline.exporter.dropped with unit "{item}" and a description

  Scenario: One metric turned off
    Given the telemetry section turns off http.server.request.duration
    When a call ends
    Then the telemetry receiver gets the metric hotline.exporter.up
    And the telemetry receiver gets no metric http.server.request.duration

  Scenario: A group of metrics turned off with a star
    Given the telemetry section turns off go.*
    When the proxy starts
    Then the telemetry receiver gets no metric starting with go.
    And the telemetry receiver gets the metric hotline.exporter.up

  Scenario: A name that matches none of the proxy's metrics is a warning
    Given the telemetry section turns off hotline.exporter.upp
    When the proxy starts
    Then the proxy logs a warning naming hotline.exporter.upp
    And the proxy runs

  Scenario: Without a telemetry section nothing of the proxy's own is sent
    Given a config with no telemetry section
    When a call ends
    Then the proxy logs a warning that its own telemetry is not sent
    And the product receiver gets no metric but hotline.requests

  Scenario: A change to the telemetry section needs a restart
    Given the proxy runs
    When the config file changes so that the telemetry section turns off go.*
    And the proxy checks its files
    Then the proxy logs a warning that telemetry needs a restart
