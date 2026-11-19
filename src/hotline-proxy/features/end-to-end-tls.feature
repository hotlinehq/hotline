@e2e
Feature: Calling over TLS to the proxy, end to end
  A real client opens TLS to the real hotline-proxy program and sends the
  full vendor address inside it. The program's certificate comes from the
  test certificate authority, which the client trusts. Case numbers are those in
  egress-proxy-options.md: cases 4 and 5 put a full address in the request
  line over TLS. The Node client comes later (Peter, 2026-10-28).

  Background:
    Given a fake OTLP receiver
    And the hotline-proxy program runs with TLS on its proxy port and integration test-shop for the test vendor

  Scenario Outline: The client matrix over TLS to the proxy
    When the <client> client sends case <case>: <how>
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the receiver got one hotline.request record with hotline.ingress forward_proxy and ID test-shop

    Examples:
      | client | case | how                                                       |
      | Go     | 4    | the https address over TLS to the proxy, using URL.Opaque |
      | Go     | 5    | the http address over TLS through Transport.Proxy         |

  @later
  Scenario Outline: The client matrix over TLS in other languages
    When the <client> client sends case <case>: <how>
    Then the app gets the test vendor's status, headers and body unchanged

    Examples:
      | client | case | how                                            |
      | Node   | 4    | https.request with servername set to the proxy |
      | Node   | 5    | the http address with https.request            |

  Scenario: The vendor URL in path ingress over TLS to the proxy
    When the Go client sends POST over TLS to the proxy with the https vendor address in the path
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress url_in_path and ID test-shop

  Scenario: The Host header ingress over HTTP/2 with TLS to the proxy
    When the Go client sends POST to the proxy with the test vendor's name in Host, over HTTP/2 with TLS
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress host_header and ID test-shop
