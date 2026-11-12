@e2e
Feature: Printing calls to the console, end to end
  The real hotline-proxy program prints each call to standard output, in
  colour when standard output is a terminal (Peter, 2026-11-11), or as one
  line of JSON with --format json (Peter, 2026-11-12).

  Background:
    Given a fake OTLP receiver

  Scenario: Not on a terminal, the lines are plain
    Given the hotline-proxy program runs with a console exporter and integration test-shop for the test vendor
    When the Go client calls the test vendor
    And the program gets SIGTERM
    Then it exits with code 0
    And standard output shows the call as two plain lines
    And standard error has plain lines, from the start to the stop

  Scenario: On a terminal, the lines are coloured
    Given the hotline-proxy program runs on a terminal 100 columns wide, with a console exporter and integration test-shop for the test vendor
    When the Go client calls the test vendor
    And the program gets SIGTERM
    Then it exits with code 0
    And the terminal shows the call in colour

  Scenario: With --format json, everything is JSON
    Given the hotline-proxy program runs with "--format json", a console exporter and integration test-shop for the test vendor
    When the Go client calls the test vendor
    And the program gets SIGTERM
    Then it exits with code 0
    And standard output shows the call as one line of JSON
    And every line on standard error is a JSON object with time, level and msg
