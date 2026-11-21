Feature: Sending OpenTelemetry data
  Records and spans go out over OTLP. Standard names come from the
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

  Scenario: No metrics go to exporters
    Exporters get records and spans only. The control plane works out
    counts and latencies from the records (Peter, 2026-11-21).
    When calls end
    Then the receiver gets nothing on /v1/metrics

  Scenario: Metric settings on an exporter are ignored
    metricInterval and temporality were the settings of the counter
    exporters once got. They mean nothing there now, so they are ignored,
    with no error and no warning (Peter, 2026-11-21).
    Given an exporter that sets metricInterval to 0s and temporality to weekly
    When a call ends
    Then the receiver gets the record
    And the proxy logs no warning about metricInterval or temporality

  Scenario: How often each exporter sends, by default
    Records and spans go out every 15 seconds, or sooner once 512 are
    waiting (Peter, 2026-11-01).
    Given an exporter that sets no intervals
    When a call ends
    Then nothing arrives within a second
    And the record and its spans arrive when the proxy stops

  Scenario Outline: Each signal has its own interval
    Given an exporter that sets <setting> to 1h
    When a call ends
    Then no <signal> arrives within a second, but the others do
    And the <signal> arrives when the proxy stops

    Examples:
      | setting        | signal |
      | logInterval    | record |
      | traceInterval  | span   |

  Scenario: Several exporters get the same data
    Given two exporters
    When a call ends
    Then both get the same record and spans

  Scenario: One exporter down
    Given two exporters, one of them down
    When calls end
    Then the other keeps receiving

  Scenario: Default queue size
    Given an exporter with no queue size set
    When the proxy starts
    Then its queue holds 10 000 records

  Scenario: A full queue refuses the newest record
    A full queue keeps what waits and refuses what is new, for records and
    spans alike (Peter, 2026-11-09). Drops are counted in a warning log
    line, at most once a minute per exporter (Peter, 2026-10-28).
    Given an exporter with queueSize 3 whose exporter is busy with the first record
    When 4 more calls end
    Then the exporter gets records 1, 2, 3 and 4
    And the proxy logs a warning that 1 record was dropped for that exporter
    And no call was blocked or slowed

  Scenario: A full queue refuses a whole call's spans
    A call's spans go into the queue together or not at all, so the queue
    never splits a trace (Peter, 2026-11-09). The first call makes more
    spans than the second, which reuses the connection; the queue has room
    for the first call only.
    Given an exporter with queueSize 20 that sends only on shutdown
    When 2 calls end
    And the proxy shuts down
    Then the exporter gets every span of the first call
    And it gets no span of the second call
    And the proxy logs a warning that spans of 1 call were dropped for that exporter

  Scenario: A call with more spans than the queue holds is dropped
    Given an exporter with queueSize 5 that sends only on shutdown
    When a call ends
    And the proxy shuts down
    Then the exporter gets no span

  Scenario: Flushing on shutdown
    Given records waiting in the queue
    When the proxy shuts down
    Then they are sent first, within a time limit
