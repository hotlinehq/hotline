@e2e
Feature: The intercept ingress, end to end
  A real Go client calls the test vendor's own https address and knows
  nothing of the proxy. A real firewall rule on this machine redirects its
  connection to the program's listen.intercept port. The program reads the
  vendor's name from the TLS handshake, and the port the client was
  calling from the operating system, which only a real redirect can
  show (Peter, 2026-11-08). The scenario is skipped where the test cannot
  change the firewall.

  Background:
    Given a fake OTLP receiver

  Scenario: A redirected call is intercepted
    Given the hotline-proxy program runs with listen.intercept and integration test-shop for the test vendor
    When the Go client sends POST to the test vendor's https address while a firewall redirects its connection to the listener
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress intercept and ID test-shop
