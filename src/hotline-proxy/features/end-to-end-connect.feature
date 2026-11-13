@e2e
Feature: The CONNECT ingress, end to end
  A real Go client calls the test vendor's own https address through the
  real hotline-proxy program, with the standard proxy setting, the way
  HTTPS_PROXY works. It sends CONNECT with the ID in the proxy address, and
  trusts the interception issuer the program signs with. Like Go's default
  client, it offers HTTP/2 to the proxy and then speaks HTTP/1.1 to it
  (Peter, 2026-10-29).

  Background:
    Given a fake OTLP receiver

  Scenario Outline: The Go client with the standard proxy setting
    Given the hotline-proxy program runs with <setup> and integration test-shop for the test vendor
    When the Go client sends POST to the test vendor's https address through the proxy at <proxy address>
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress connect and ID test-shop

    Examples:
      | setup                                | proxy address |
      | the connect part                       | an http one   |
      | the connect part and TLS on its port   | an https one  |
