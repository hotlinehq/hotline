Feature: Sending OpenTelemetry data
  Records and counts go out over OTLP. Standard names come from the
  semconv Go package; Hotline-only fields use the hotline. prefix.
  Tests use a fake OTLP receiver running locally.

  Background:
    Given a fake OTLP receiver

  Scenario: Resource on every signal
    When the proxy sends any signal
    Then its resource has service.name hotline-proxy, service.version and service.instance.id

  Scenario: Labels from the config
    Each exporter may set its own resource labels, to say where the proxy
    runs. A value is written out, or read from { env: NAME } or
    { file: path }. service.name and service.instance.id may be changed;
    service.version is always the proxy's own (Peter, 2026-10-30).
    Given an exporter with the resource service.name invoices-proxy, service.instance.id from the env POD_NAME and deployment.environment.name from the file env-name
    When the proxy sends any signal
    Then its resource has service.name invoices-proxy, service.instance.id from POD_NAME and deployment.environment.name from the file
    And service.version is still the proxy's own
    And the hotline.requests counter carries the same resource

  Scenario: Each exporter has its own labels
    Given two exporters, the second with the resource deployment.environment.name staging
    When a call ends
    Then the first gets the proxy's own resource, with no deployment.environment.name
    And the second gets deployment.environment.name staging

  Scenario: One log record per call
    When a call ends
    Then the receiver gets one OTLP log record with all of the record's attributes
    And standard names come from the semconv constants, such as http.request.method and url.full
    And every other field has the exact hotline. name from the spec
    And numbers are numbers, true and false are booleans and header values are lists of strings

  Scenario: The hotline.requests counter
    When a call ends
    Then hotline.requests goes up by one, with unit {request}
    And it carries hotline.integration.id, http.request.method, http.response.status_code and error.type

  Scenario: The counter has no url.template
    The template is in every record, but a counter label per route would
    make a series for each route of each spec, too many for a big spec such as
    AWS's (Peter, 2026-11-07).
    When a call ends
    Then hotline.requests carries no url.template

  Scenario Outline: Temporality
    Given an exporter that <sets>
    When calls end
    Then the totals are <kind>

    Examples:
      | sets                       | kind       |
      | does not set temporality   | cumulative |
      | sets temporality to delta  | delta      |

  Scenario: How often each exporter sends, by default
    Counts go out once a minute: one data point a minute is what Grafana
    Cloud's standard pricing covers. Records and spans go out every 15
    seconds, or sooner once 512 are waiting (Peter, 2026-11-01).
    Given an exporter that sets no intervals
    When a call ends
    Then nothing arrives within a second
    And the record, its spans and the count arrive when the proxy stops

  Scenario Outline: Each signal has its own interval
    Given an exporter that sets <setting> to 1h
    When a call ends
    Then no <signal> arrives within a second, but the others do
    And the <signal> arrives when the proxy stops

    Examples:
      | setting        | signal |
      | metricInterval | count  |
      | logInterval    | record |
      | traceInterval  | span   |

  Scenario: No latency histogram
    When calls end
    Then no histogram is sent

  Scenario: Several exporters get the same data
    Given two exporters
    When a call ends
    Then both get the same record and count

  Scenario: One exporter down
    Given two exporters, one of them down
    When calls end
    Then the other keeps receiving

  Scenario: Default queue size
    Given an exporter with no queue size set
    When the proxy starts
    Then its queue holds 10 000 records

  Scenario: A full queue drops the oldest
    Until the proxy's own health data exists, drops are counted in a
    warning log line, at most once a minute per destination (Peter,
    2026-10-28).
    Given an exporter with queueSize 3 whose destination is busy with the first record
    When 4 more calls end
    Then the destination gets records 1, 3, 4 and 5
    And the proxy logs a warning that 1 record was dropped for that destination
    And no call was blocked or slowed

  Scenario: Flushing on shutdown
    Given records waiting in the queue
    When the proxy shuts down
    Then they are sent first, within a time limit
