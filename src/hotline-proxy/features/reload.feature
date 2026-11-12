Feature: Reloading the config without a restart
  The proxy checks its files every 5 seconds, and a HUP signal reloads at
  once. The files are what it read at load: the config, the specs, the
  overlays, the key files and the certificates. A file counts as changed
  when it is another file than before, or its time or size changed; it is
  read in full only then. The new config is checked like at start and used
  only if all of it is valid; new calls use it, calls in progress finish on
  the old one. A broken config is not used and is logged as a warning.
  Listen addresses and exporters need a restart. Every record carries a
  fingerprint of the config, specs and overlays (Peter, 2026-10-31).

  Background:
    Given a config with integration talon-shop-a for Talon.One

  Scenario: A changed config is used once the proxy checks its files
    When an app calls Talon.One as talon-shop-b
    And the config file changes so that talon-shop-b also uses Talon.One
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-b
    Then record 1 has no url.template
    And record 2 has url.template /v2/customer_sessions/{customerSessionId}
    And the proxy logs that the config was reloaded

  Scenario: A reload is asked for directly
    When the config file changes so that talon-shop-b also uses Talon.One
    And the proxy is told to reload
    And an app calls Talon.One as talon-shop-b
    Then record 1 has url.template /v2/customer_sessions/{customerSessionId}

  Scenario: Files that did not change change nothing
    When an app calls Talon.One as talon-shop-a
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-a
    Then record 1 and record 2 carry the same hotline.config.version
    And the proxy does not log that the config was reloaded

  Scenario Outline: A broken config is not used
    When the config file changes so that <problem>
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-a
    Then record 1 has url.template /v2/customer_sessions/{customerSessionId}
    And the proxy logs a warning that the reload failed, naming <named>

    Examples:
      | problem                               | named              |
      | it is not valid YAML                  | is not valid YAML  |
      | talon-shop-a's spec file is missing   | cannot be read     |

  Scenario: A fixed config after a broken one is used
    When the config file changes so that it is not valid YAML
    And the proxy checks its files
    And the config file changes so that talon-shop-b also uses Talon.One
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-b
    Then record 1 has url.template /v2/customer_sessions/{customerSessionId}

  Scenario: A changed spec alone is noticed, and changes the version
    When an app calls Talon.One as talon-shop-a
    And the Talon.One spec file changes so that it has no paths
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-a
    Then record 2 has no url.template
    And record 1 and record 2 carry different hotline.config.version

  Scenario: An overlay named only by the new config is watched until it arrives
    When the config file changes so that Talon.One has the overlay no-paths.yaml
    And the proxy checks its files
    Then the proxy logs a warning that the reload failed, naming no-paths.yaml
    When the overlay file no-paths.yaml arrives, removing every path
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-a
    Then record 1 has no url.template

  Scenario: A changed key file is noticed
    Given talon-shop-a's fingerprintKey is read from a file
    When an app calls Talon.One as talon-shop-a
    And talon-shop-a's key file changes to another key
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-a
    Then record 1 and record 2 carry different hotline.signature_key_id

  Scenario Outline: Listen addresses and exporters need a restart
    When the config file changes so that <change>, and talon-shop-b also uses Talon.One
    And the proxy checks its files
    And an app calls Talon.One as talon-shop-b
    Then the proxy logs a warning that <setting> needs a restart
    And record 1 has url.template /v2/customer_sessions/{customerSessionId}

    Examples:
      | change                                | setting       |
      | listen.proxy moves to another port    | listen.proxy  |
      | listen.admin moves to another port    | listen.admin  |
      | there is one more exporter            | exporters     |
      | listen.tls is added                   | listen.tls    |
      | listen.connect is added               | listen.connect |
      | listen.intercept is added             | listen.intercept |
      | listen.capture is added               | listen.capture |
      | telemetry.pprof is turned on          | telemetry.pprof |
