Feature: The Host header ingress
  The app calls the proxy with a plain path and names the vendor in the
  Host header (in HTTP/2, the :authority field). A normal HTTP request
  sends no scheme, so the proxy picks https unless told otherwise
  (Peter, 2026-10-29).

  Scenario Outline: A plain path is the Host header ingress
    Given the proxy is running
    When an app sends "PUT /v2/customer_sessions/1" with "Host: <host header>"
    Then the call uses the Host header ingress
    And the vendor is <host> on port <port>

    Examples:
      | host header        | host          | port |
      | api.talon.one      | api.talon.one | 443  |
      | api.talon.one:8443 | api.talon.one | 8443 |
      | API.TALON.ONE      | api.talon.one | 443  |

  Scenario: What the vendor receives
    Given an app call with Proxy-Authorization, Authorization, Connection and a JSON body
    When the proxy forwards it to "/v2/customer_sessions/sess-1?dry=true&b=%2F" with "Host: api.talon.one"
    Then the method, path, query and body reach the vendor unchanged
    And the vendor gets "Host: api.talon.one"
    And Proxy-Authorization is removed
    And Authorization and every other end-to-end header are kept
    And headers for one hop only, such as Connection and Proxy-Connection, are removed as HTTP requires
    And the proxy adds no header of its own, such as Accept-Encoding or User-Agent

  Scenario Outline: Choosing http or https to the vendor
    Given <settings>
    When an app sends "GET /v2/x" with "Host: <host header>"
    Then the proxy calls the vendor over <scheme> on port <port>

    Examples:
      | settings                                            | host header        | scheme | port |
      | an api with TLS origination on, the default         | api.talon.one      | https  | 443  |
      | an api with TLS origination on, the default         | api.talon.one:8080 | https  | 8080 |
      | an api with TLS origination on, the default         | api.talon.one:80   | https  | 80   |
      | an api with tlsOrigination set to false             | api.talon.one      | http   | 80   |
      | a not-passed call with the built-in settings        | api.x.com          | https  | 443  |
      | a not-passed entry with tlsOrigination set to false | api.x.com          | https  | 443  |

  Scenario: The record of a Host header call
    When an app sends "PUT /v2/customer_sessions/sess-1" with "Host: api.talon.one"
    Then the record says hotline.ingress host_header
    And url.full is "https://api.talon.one/v2/customer_sessions/REDACTED"
    And server.address is api.talon.one and server.port is 443
    And nothing in the record names the proxy's own address

  Scenario: A not-passed call gets the minimal record
    Given a not-passed call with the built-in settings
    When an app sends "GET /a" with "Host: api.x.com"
    Then the record holds only hotline.integration.id, hotline.ingress, client.address, server.address, network.peer.address, http.request.method, http.response.status_code, http.request.body.size, http.response.body.size, error.type and hotline.timing.total_us
    And the record's ID is not-passed
    And the record says hotline.ingress host_header

  Scenario: A call with no Host is refused
    When an app sends "GET /v2/x" over HTTP/1.0 with no Host header
    Then the proxy answers 400 with "Proxy-Status: hotline; error=http_request_error"
    And nothing is sent to any vendor
    And the record is minimal with hotline.ingress host_header, http.response.status_code 400 and error.type http_request_error

  Scenario: A call to the proxy itself is stopped by the guard
    When an app sends a Host header call to the proxy's own address
    Then the proxy answers 403 with "Proxy-Status: hotline; error=destination_ip_prohibited"
    And the proxy opens no connection

  Scenario: Plain HTTP/2 by prior knowledge
    Given an app that speaks HTTP/2 straight away without TLS
    When it sends "GET /v2/x" with ":authority api.talon.one"
    Then the call uses the Host header ingress
    And the vendor gets the call over https

  Scenario: HTTP/2 with a vendor address in the path
    Given an app that speaks HTTP/2 straight away without TLS
    When it sends "GET /https://api.talon.one/v2/x" with ":authority localhost:8080"
    Then the call uses the vendor URL in path ingress
    And the vendor gets "Host: api.talon.one"

  Scenario: HTTP/2 never makes a plain call to the vendor
    Given an api with tlsOrigination set to false
    And an app that speaks HTTP/2 straight away without TLS
    When it sends "GET /v2/x" with ":authority api.talon.one" and ":scheme http"
    Then the proxy calls the vendor over https on port 443

  Scenario: The older Upgrade: h2c handshake is ignored
    When an app sends "GET /v2/x" with "Host: api.talon.one" and "Upgrade: h2c"
    Then the connection stays HTTP/1.1
    And the vendor gets the call

  Scenario: An app that offers HTTP/2 over TLS gets HTTP/2
    Given the proxy port uses TLS with a certificate the app trusts
    And an app that trusts the test issuer and offers HTTP/2 and HTTP/1.1
    When the app sends "GET /v2/x" with ":authority api.talon.one" over TLS to the proxy
    Then the connection uses HTTP/2
    And the app gets the vendor's status, headers and body unchanged
