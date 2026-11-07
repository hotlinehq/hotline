@e2e
Feature: Profile pages on the admin port
  Go's profile pages (where processor time and memory go) can be served on
  the admin port, to find out why a proxy is slow. They are off by default:
  the admin port has no password, and a processor profile makes the proxy do
  extra work while it is taken. `telemetry.pprof: true` turns them on, next to
  the proxy's own telemetry settings, without a section of their own. They
  are /debug/pprof/ and, with the exact total of bytes the proxy has handed
  out, /debug/vars. Changing the setting needs a restart. It does nothing
  without a listen.admin address, and that is a warning (Peter, 2026-11-07).

  Scenario: Off by default, the admin port has no profile pages
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor
    Then GET /debug/pprof/ on the admin port answers 404
    And GET /debug/vars on the admin port answers 404
    And GET /livez on the admin port answers 200
    When the program gets SIGTERM
    Then it exits with code 0

  Scenario: Turned on, the admin port serves the profile pages
    Given a fake OTLP receiver
    And the telemetry section turns on the profile pages
    And the hotline-proxy program runs with integration test-shop for the test vendor
    Then GET /debug/pprof/ on the admin port answers 200
    And GET /debug/pprof/heap on the admin port answers 200
    And GET /debug/pprof/goroutine on the admin port answers 200
    And GET /debug/vars on the admin port answers 200
    And GET /livez on the admin port answers 200
    When the program gets SIGTERM
    Then it exits with code 0
