@e2e
Feature: Reloading the config of the running program
  The program checks its files every 5 seconds, and SIGHUP reloads at once.
  Calls in progress finish on the old config; new calls use the new one
  (Peter, 2026-10-31).

  Scenario: SIGHUP reloads at once, and a call in progress finishes on the old config
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    And the test vendor holds its answer
    When an app's call is in progress
    And the config file changes
    And the program gets SIGHUP
    Then within 2 seconds standard error says "config reloaded"
    When the test vendor then answers
    Then the app gets the test vendor's answer
    When a new call through the proxy reaches the test vendor
    And the program gets SIGTERM
    Then it exits with code 0
    And the records carry two different config fingerprints

  Scenario: A changed config file is picked up with no signal
    The file changes as soon as the proxy port listens, while the proxy
    may still be starting. Each file is noted just before it is read, so a
    change made after that, even during startup, is picked up.
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    When the config file changes
    Then within 8 seconds standard error says "config reloaded"
    When a new call through the proxy reaches the test vendor
    And the program gets SIGTERM
    Then it exits with code 0
    And the records carry one config fingerprint, not the first one
