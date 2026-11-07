# Answers to the open questions are in docs/proxy-slice-1.md.
Feature: Loading the config file
  The proxy reads one YAML file, hotline-proxy.yaml, at start and again on
  every reload (see reload.feature).

  Scenario: A valid config is read in full
    Given a valid config file
    When the proxy loads it
    Then the proxy listen address is read
    And every OTLP exporter is read with its endpoint
    And every api is read with its hosts and the spec it points to
    And every integration is read with the apis it names
    And spec files are found relative to the folder of the config file

  Scenario: The config's file name and fingerprint go into every record
    The label is the config file's name, without its folder, a + and a
    fingerprint: the first 12 hex characters of a SHA-256 of the config,
    specs and overlays (Peter, 2026-10-31). The fingerprint changes with
    every edit and the name says which config it is, so the config has no
    version field of its own (Peter, 2026-11-01).
    Given a valid config file named hotline-proxy.aws-shop.yaml
    When the proxy loads it
    Then every record carries hotline.config.version hotline-proxy.aws-shop.yaml and a fingerprint

  Scenario: The full example loads
    Given the full example config from proxy-example
    When the proxy loads it
    Then it loads together with both of its specs

  Scenario Outline: Exporter temporality
    Given an exporter that <sets>
    When the proxy loads the config
    Then the exporter's temporality is <temporality>

    Examples:
      | sets                        | temporality |
      | does not set temporality    | cumulative  |
      | sets temporality to delta   | delta       |

  Scenario Outline: Api defaults
    Given an api that <sets>
    When the proxy loads the config
    Then <result>

    Examples:
      | sets                            | result                                       |
      | does not set tlsOrigination     | TLS origination is on                        |
      | sets tlsOrigination to false    | TLS origination is off                       |
      | does not set vendorTimeout      | the proxy waits 60 seconds for the vendor    |
      | sets vendorTimeout to 10s       | the proxy waits 10 seconds for the vendor    |

  Scenario: One api with several hosts
    Given an api that lists the hosts api.talon.one and eu.api.talon.one
    When the proxy loads the config
    Then calls to either host use that api

  Scenario: Hosts match in any case
    Given an api that lists the host API.Talon.One
    When a call goes to api.talon.one
    Then that api is used

  Scenario: Integrations share an api by name
    Each vendor api is written once in the apis section, and integrations
    name the apis they use (Peter, 2026-10-29).
    Given the api talon-one with the host api.talon.one and the Talon.One spec
    And the integrations talon-shop-a and talon-shop-b both naming talon-one
    When either integration calls api.talon.one
    Then both calls use the Talon.One spec

  Scenario: Hundreds of integrations share one api
    A spec is read once and shared by every integration that names its api.
    Given 300 integrations that all name the api talon-one
    When the proxy loads the config
    Then calls from the first and the last of them use the Talon.One spec

  Scenario: An integration may change only vendorTimeout
    Given the api talon-one with vendorTimeout 10s
    And talon-shop-a naming talon-one as it is
    And talon-shop-b naming talon-one with vendorTimeout 5s
    Then calls from talon-shop-a wait 10 seconds for the vendor
    And calls from talon-shop-b wait 5 seconds for the vendor

  Scenario: Built-in not-passed settings
    Given a config file with no not-passed entry
    When the proxy loads it
    Then not-passed calls use TLS origination and may go to any host

  Scenario: A not-passed entry overrides the built-in settings
    Given a config file with a not-passed entry that sets tlsOrigination to false
    When the proxy loads it
    Then not-passed calls use no TLS origination

  Scenario: The same host in two apis of different integrations
    Given two integrations that each name an api for api.talon.one, each with its own spec
    When the proxy loads the config
    Then both are accepted

  Scenario: A config with no integrations
    Given a config file with exporters but no integrations
    When the proxy loads it
    Then it is accepted and every call uses the not-passed settings

  Scenario Outline: Broken configs are refused
    Given a config file where <problem>
    When the proxy loads it
    Then the config is refused with a message naming the problem

    Examples:
      | problem                                              |
      | the file does not exist                              |
      | the file is not valid YAML                           |
      | there is no proxy listen address                     |
      | an integration has no id                             |
      | there are no exporters                               |
      | an exporter's temporality is neither cumulative nor delta |
      | an exporter's endpoint is not a valid address        |
      | an exporter's queueSize is zero                      |
      | an exporter's metricInterval is zero                 |
      | an exporter's logInterval is not a duration          |
      | an exporter's traceInterval is negative              |
      | an exporter's protocol is http/json                  |
      | an HTTP exporter's endpoint has no http:// or https:// |
      | an exporter's endpoint has a query                   |
      | an exporter's endpoint has a user name in it         |
      | a gRPC exporter's endpoint has a path                |
      | an exporter sets tls on a plain http:// endpoint     |
      | an exporter sets tls.cert without tls.key            |
      | an exporter's tls.ca file does not exist             |
      | an exporter's tls.ca file holds no certificate       |
      | an exporter's tls.cert file does not exist           |
      | an exporter's header is neither a value, a file nor an env |
      | an exporter's header secret file does not exist      |
      | an exporter's header environment variable is not set |
      | an exporter's compression is neither gzip nor none   |
      | an exporter's resource sets service.version          |
      | an exporter's resource has the name "my label"       |
      | an exporter's resource environment variable is not set |
      | an integration's fingerprintKey is written out       |
      | an integration's fingerprintKey file does not exist  |
      | two integrations have the same ID                    |
      | one integration names two apis with the same host    |
      | an integration names an api that is not in apis      |
      | an integration names an api with a list, not a name  |
      | an api has no spec                                   |
      | an api points to a spec file that does not exist     |
      | a spec is not an OpenAPI document                    |
      | a spec is OpenAPI 3.0                                |
      | a schema in a spec has the type integr               |
      | a $ref in a spec points nowhere                      |
      | a $ref in a spec points to another file              |
      | a vendorTimeout is zero                              |
      | a vendorTimeout is negative                          |
      | a vendorTimeout is not a duration                    |
      | an x-hotline-redact mark is not true or false        |
      | an integration's traceFormat is zipkin               |

  Scenario Outline: Settings the proxy does not use start with a warning
    The proxy starts anyway, so a typo never stops traffic, but each one is
    named in a warning log line (Peter, 2026-10-28).
    Given a config file where <problem>
    When the proxy loads it
    Then the proxy starts and logs a warning naming <name>

    Examples:
      | problem                                               | name          |
      | the not-passed entry has an apis list                 | apis          |
      | a field name has a typo, such as tlsOrigintion        | tlsOrigintion |
      | an integration changes tlsOrigination of an api it names | tlsOrigination |
      | there is no admin listen address                      | listen.admin  |
      | telemetry.pprof is on but there is no admin listen address | telemetry.pprof |
      | the config still has a version field                  | version       |

  Scenario: A busy listen address
    Given another program listens on the proxy address
    When the proxy loads the config
    Then the proxy does not start, and says the address is in use

  Scenario: A busy admin address
    Given another program listens on the admin address
    When the proxy loads the config
    Then the proxy does not start, and says the address is in use

  Scenario: Every problem is listed
    Given a config file with three problems
    When the proxy loads it
    Then all three problems are listed, not only the first one

  Scenario: No secret in error messages
    Given a broken config file with an integration that has a fingerprint key
    When the proxy reports the problem
    Then the message holds no secret value
