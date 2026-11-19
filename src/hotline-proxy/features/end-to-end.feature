@e2e
Feature: Calling through the proxy, end to end
  A real client calls the real hotline-proxy program, which calls a local
  TLS test vendor; a fake OTLP receiver collects what the program sends.
  The program trusts the test vendor's certificate authority through --trust-file. The Go
  client cases are copied from the research folder (matrix-clients/go);
  case numbers are those in egress-proxy-options.md. Only cases 1, 2, 4 and
  5 put a full address in the request line. The Node, Java and Rust clients
  come later (Peter, 2026-10-28).

  Background:
    Given a fake OTLP receiver
    And the hotline-proxy program runs with integration test-shop for the test vendor

  Scenario Outline: The client matrix
    When the <client> client sends case <case>: <how>
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the receiver got one hotline.request record with hotline.ingress forward_proxy and ID test-shop
    And the receiver saw hotline.requests go up by one
    And the record holds the redacted url.full and no secret

    Examples:
      | client | case | how                                                  |
      | Go     | 1    | POST https://… over plain HTTP/1.1, using URL.Opaque |
      | Go     | 2    | POST http://… through Transport.Proxy                |

  @later
  Scenario Outline: The client matrix in other languages
    When the <client> client sends case <case>: <how>
    Then the app gets the test vendor's status, headers and body unchanged

    Examples:
      | client | case | how                                                         |
      | Node   | 1    | POST https://… with http.request and a full URL as the path |
      | Node   | 2    | POST http://… with http.request                             |

  Scenario Outline: Failures end to end
    When the Go client calls <target>
    Then <result>

    Examples:
      | target                                      | result                                                                                  |
      | a vendor with an untrusted certificate      | the app gets 502 with Proxy-Status and the record's error.type is tls_certificate_error |
      | a host that does not exist                  | the app gets 502 with Proxy-Status and the record's error.type is dns_error             |
      | the test vendor without Proxy-Authorization | the call reaches the vendor and the record is minimal with ID not-passed                |

  Scenario Outline: The vendor URL in path ingress
    The app sets its base address to the proxy followed by the vendor's
    address (Peter, 2026-10-29).
    When the Go client sends POST to the proxy with the <scheme> vendor address in the path
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress url_in_path and ID test-shop

    Examples:
      | scheme |
      | https  |
      | http   |

  Scenario Outline: The Host header ingress
    The app sets its base address to the proxy and names the vendor in the
    Host header (Peter, 2026-10-29).
    When the Go client sends POST to the proxy with the test vendor's name in Host, over <how>
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress host_header and ID test-shop

    Examples:
      | how            |
      | plain HTTP/1.1 |
      | plain HTTP/2   |

  Scenario Outline: The transparent redirect
    The app keeps the vendor's own name and only turns https into http.
    Its name lookup points the vendor's name at the proxy, as hostAliases
    would in the app's pod, and it adds Proxy-Authorization as a default
    header. To the proxy it is a Host header call (Peter, 2026-10-29).
    When the Go client sends POST to the test vendor's name over http, with that name pointing at the proxy, over <how>
    Then the app gets the test vendor's status, headers and body unchanged
    And the test vendor got the call over https and never saw Proxy-Authorization
    And the test vendor got its own name in Host
    And the receiver got one hotline.request record with hotline.ingress host_header and ID test-shop

    Examples:
      | how            |
      | plain HTTP/1.1 |
      | plain HTTP/2   |
