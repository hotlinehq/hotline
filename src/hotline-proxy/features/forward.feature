Feature: Forwarding a call to the vendor
  The forwarder makes the call to the vendor and hands the response back.
  Tests use local vendors from net/http/httptest, never the real internet.

  Scenario Outline: Choosing http or https to the vendor
    Given <settings>
    When an app sends "GET <address>"
    Then the proxy calls the vendor over <scheme> on port <port>

    Examples:
      | settings                                            | address                    | scheme | port |
      | an api with TLS origination on, the default         | https://api.talon.one/v2/x | https  | 443  |
      | an api with TLS origination on, the default         | http://api.talon.one/v2/x  | https  | 443  |
      | an api with tlsOrigination set to false             | http://api.talon.one/v2/x  | http   | 80   |
      | a not-passed call with the built-in settings        | http://api.x.com/a         | https  | 443  |
      | a not-passed entry with tlsOrigination set to false | http://api.x.com/a         | http   | 80   |

  Scenario: Upgrading an address with an explicit port
    Given an api with TLS origination on, the default
    When an app sends "GET http://api.talon.one:8080/v2/x"
    Then the proxy calls the vendor over https on port 8080

  Scenario: A not-passed https call stays https
    Given a not-passed call with the built-in settings
    When an app sends "GET https://api.x.com/a"
    Then the proxy calls the vendor over https on port 443

  Scenario: What the vendor receives
    Given an app call with Proxy-Authorization, Authorization, Connection and a JSON body
    When the proxy forwards it
    Then the method, path, query and body reach the vendor unchanged
    And Proxy-Authorization is removed
    And Authorization and every other end-to-end header are kept
    And headers for one hop only, such as Connection and Proxy-Connection, are removed as HTTP requires
    And Host is set to the vendor host from the request line
    And the proxy adds no header of its own, such as Accept-Encoding or User-Agent

  Scenario Outline: A vendor response that goes quiet
    vendorTimeout also limits how long a response may go quiet between its
    parts (Peter, 2026-10-28), so slow responses that keep coming still work.
    Given a vendor with a vendorTimeout of 10 seconds whose response goes quiet after its first part
    When the response stays quiet for <quiet> seconds
    Then <result>

    Examples:
      | quiet | result                                                                          |
      | 9     | the app gets the whole response and the record has no error.type                |
      | 10    | the response to the app breaks off and the record says http_response_incomplete |

  Scenario: The vendor answers before the app has finished sending
    Given a vendor that answers 413 without reading the body
    When an app sends a body slowly, in parts
    Then the app gets 413
    And the record says http.response.status_code 413

  Scenario: A WebSocket upgrade passes through
    A WebSocket is a connection that stays open and carries messages both
    ways. The proxy passes it through; checking its messages comes later.
    Given a vendor that accepts a WebSocket upgrade and sends back what it gets
    When an app opens a WebSocket through the proxy and sends "ping"
    Then the app gets 101 Switching Protocols and "ping" back
    And the record says http.response.status_code 101

  Scenario: A WebSocket that ends logs no warning
    Once the forwarder has taken over the app's connection for the
    WebSocket, the handler must not flush it: Go's server would crash on
    that, catch the crash itself and log a warning with a stack trace,
    for every WebSocket (found by the WebSocket spike, 2026-11-18).
    Given a vendor that accepts a WebSocket upgrade and sends back what it gets
    When an app opens a WebSocket through the proxy and sends "ping"
    Then the record says http.response.status_code 101
    And the proxy's HTTP server logged nothing

  Scenario: A WebSocket to a checked api is valid
    After the 101, the connection carries the WebSocket's frames, not a
    response body, so the response check has no body to read. Before, it read the
    vendor's frames as the body and failed with not_in_spec (found by the
    WebSocket spike, 2026-11-18).
    Given a shop spec where GET /orders answers 101 Switching Protocols
    And the shop vendor accepts a WebSocket upgrade, sends "bye" and closes at once
    When an app opens a WebSocket to the shop through the proxy, reads "bye" and closes, 1 times
    Then the record says hotline.schema.status=valid

  Scenario: A WebSocket's opening call hides its key under full content
    Given a file exporter
    And a shop spec where GET /orders answers 101 Switching Protocols, with content full and the key header X-Goog-Api-Key in its security section
    And the shop vendor accepts a WebSocket upgrade, sends "bye" and closes at once
    When an app opens a WebSocket to the shop through the proxy with "X-Goog-Api-Key: k-123", reads "bye" and closes
    Then the line has hotline.content.request.header.x-goog-api-key REDACTED
    And the line has hotline.content.request.header.authorization REDACTED

  Scenario: A WebSocket that ends from both sides at once is recorded once
    As a WebSocket ends, the proxy reads the vendor's side to its end
    while the end of the call closes it, both at the same moment. Each
    WebSocket must still give one record, with no unguarded access for the
    race detector to find when the tests run with it. The shop's response is checked, so the end also
    starts the response check.
    Given the shop spec
    And the shop vendor accepts a WebSocket upgrade, sends "bye" and closes at once
    When an app opens a WebSocket to the shop through the proxy, reads "bye" and closes, 20 times
    Then there are 20 records, each with http.response.status_code 101

  Scenario: A request trailer reaches the vendor
    A trailer is a header sent after the body, such as a checksum of an
    upload that is only known at its end. Go's own reverse proxy loses its
    value (Go issue 82939).
    Given a vendor that reads the trailers of the request
    When an app sends a body in parts with the trailer "X-Checksum: crc32=3610a686"
    Then the vendor gets the trailer "X-Checksum: crc32=3610a686"

  Scenario: Response trailers pass back, but not fields forbidden after the body
    gRPC responses end with trailers such as Grpc-Status. Fields that change
    how a response is read, such as Content-Type, must never come after the
    body (RFC 9110, section 6.5.1; Go issues 82649 and 82450).
    Given a vendor that ends its response with the trailers "Grpc-Status: 5", "Content-Type: text/evil" and "Www-Authenticate: Basic"
    When an app calls it through the proxy
    Then the app gets the trailer "Grpc-Status: 5"
    And the app gets no trailer Content-Type or Www-Authenticate

  Scenario: An early response passes back without headers for one hop only
    An early response, such as 103 Early Hints, lets the app start loading
    what it will need before the real answer comes. Headers for one hop
    only are removed from it, as from the final response (Go issue 81319).
    Given a vendor that sends 103 Early Hints with a Link header and Connection, X-Nominated and Keep-Alive headers, then 200
    When an app calls it through the proxy and reads every response
    Then the app gets 103 with the Link header but no Connection, X-Nominated or Keep-Alive header
    And then the app gets 200

  Scenario: No forwarding headers are added
    Given an app call
    When the proxy forwards it
    Then the vendor gets no X-Forwarded-For or Via header

  Scenario: Large bodies stream both ways
    Given a vendor that answers slowly with a large response
    When an app sends a large request body
    Then the body reaches the vendor before the app has finished sending it
    And the response reaches the app before the vendor has finished sending it

  Scenario: No retries
    Given a vendor that fails the first call
    When an app sends one call
    Then the vendor sees exactly one call

  Scenario Outline: Vendor responses pass back unchanged
    Given a vendor that answers <status> with headers and a body
    When an app calls it through the proxy
    Then the app gets <status> with the same headers and body
    And there is no Proxy-Status header

    Examples:
      | status |
      | 200    |
      | 400    |
      | 429    |
      | 503    |

  Scenario Outline: HTTP versions
    Given <app> and a vendor that offers <vendor>
    When the call goes through
    Then the proxy talks <to vendor> to the vendor and <to app> to the app

    Examples:
      | app                   | vendor              | to vendor | to app   |
      | an app using HTTP/1.1 | HTTP/2 and HTTP/1.1 | HTTP/2    | HTTP/1.1 |
      | an app using HTTP/1.1 | HTTP/1.1 only       | HTTP/1.1  | HTTP/1.1 |

  Scenario: A trusted certificate is accepted
    Given a vendor whose certificate authority is in the system trust store
    When an app calls it through the proxy
    Then the call goes through

  Scenario Outline: Bad certificates are refused
    Given a vendor whose certificate is <problem>
    When an app calls it through the proxy
    Then the app gets 502 with "Proxy-Status: hotline; error=tls_certificate_error"

    Examples:
      | problem               |
      | self-signed           |
      | expired               |
      | for another host name |

  Scenario: The certificate check cannot be turned off
    Given the config file format
    When anyone looks for a setting to skip the certificate check
    Then there is none

  Scenario Outline: The proxy's own errors
    Given <situation>
    When an app calls through the proxy
    Then the app gets <status> with "Proxy-Status: hotline; error=<error>"
    And the record's error.type is <error>

    Examples:
      | situation                                                          | status | error                    |
      | a vendor host that does not exist                                  | 502    | dns_error                |
      | a DNS lookup that takes too long                                   | 504    | dns_timeout              |
      | a vendor that refuses the connection                               | 502    | connection_refused       |
      | a vendor that takes too long to connect                            | 504    | connection_timeout       |
      | a TLS handshake that fails for a reason other than the certificate | 502    | tls_protocol_error       |
      | a vendor slower than vendorTimeout                                 | 504    | http_response_timeout    |
      | a vendor whose network cannot be reached                           | 502    | destination_unavailable  |
      | a TLS handshake that takes too long                                | 504    | connection_timeout       |
      | a vendor that closes the connection before answering               | 502    | http_response_incomplete |

  Scenario: The vendor breaks off mid-response
    Given a vendor that stops after sending half of its response
    When an app calls it through the proxy
    Then the response to the app is cut
    And the record's error.type is http_response_incomplete

  Scenario: Refused calls never reach the vendor
    Given a call the proxy refuses
    When the app sends it
    Then the vendor gets no part of it

  Scenario: The app gives up first
    Given a vendor that answers after 300 milliseconds and a vendorTimeout of 10 seconds
    When the app disconnects after 100 milliseconds
    Then the proxy keeps waiting for the vendor
    And the record holds the vendor's real status and timings
    And the record says hotline.client.cancelled true

  Scenario: The app waits for the answer
    Given a vendor that answers normally
    When the app waits for the answer
    Then the record says hotline.client.cancelled false
