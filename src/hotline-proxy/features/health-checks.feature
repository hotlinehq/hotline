@e2e
Feature: Health checks on the admin port
  Kubernetes asks two questions on the admin port, over plain HTTP: /livez,
  "is the process alive?", and /readyz, "should I send you calls?". A yes is
  200, a no is 503. The admin port listens only once the config is loaded
  and the proxy port listens, and /readyz says no from the first stop signal
  on. An exporter that is down never makes it say no. After the stop
  signal the proxy keeps taking new calls for --drain-delay (5 seconds
  unless set), since Kubernetes needs a moment to stop sending calls to it.
  The admin port answers only these two paths, and its calls make no
  records (Peter, 2026-10-31).

  Scenario: A started proxy is alive and ready
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    Then GET /livez on the admin port answers 200
    And GET /readyz on the admin port answers 200

  Scenario: An exporter that is down keeps the proxy ready
    Given a fake OTLP receiver that refuses connections
    And the hotline-proxy program runs with integration test-shop for the test vendor
    Then GET /readyz on the admin port answers 200

  Scenario: The stop signal makes the proxy not ready at once
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    And the test vendor holds its answer
    When an app's call is in progress and the program gets SIGTERM
    Then GET /readyz on the admin port answers 503
    And GET /livez on the admin port answers 200
    When the test vendor then answers
    Then the app gets the test vendor's answer
    And it exits with code 0

  Scenario: New calls are still taken for a short time after the stop signal
    Given a fake OTLP receiver
    And the hotline-proxy program runs with "--drain-delay 2s" and integration test-shop for the test vendor
    When the program gets SIGTERM
    Then GET /readyz on the admin port answers 503
    And a new call through the proxy reaches the test vendor
    And the proxy port stops taking calls between 1 and 4 seconds after the signal
    And it exits with code 0

  Scenario: A second signal skips the wait
    Given a fake OTLP receiver
    And the hotline-proxy program runs with "--drain-delay 20s" and integration test-shop for the test vendor
    When the program gets SIGTERM
    And the program gets SIGTERM again
    Then it exits with code 1 within 5 seconds
    And standard error says "stop cut short"

  Scenario Outline: The admin port answers only its health checks
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    Then <method> <path> on the admin port answers <status>

    Examples:
      | method | path    | status |
      | HEAD   | /livez  | 200    |
      | HEAD   | /readyz | 200    |
      | GET    | /       | 404    |
      | POST   | /readyz | 405    |

  Scenario: Health checks make no records
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    When GET /readyz on the admin port is asked 3 times
    And the program gets SIGTERM
    Then it exits with code 0
    And the receiver got no records
