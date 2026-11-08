Feature: The vendor URL in path ingress
  The app puts the whole vendor address after the proxy's own address, so
  it only changes the base address it calls:
  http://localhost:8080/https://api.talon.one. Only the clean /http:// or
  /https:// form is this ingress. After the ingress, the call takes the
  same steps as a forward proxy call (Peter, 2026-10-29).

  Scenario Outline: A vendor address in the path is the vendor URL in path ingress
    Given the proxy is running
    When an app sends "PUT <path>" with "Host: localhost:8080"
    Then the call uses the vendor URL in path ingress
    And the vendor is <host> on port <port>

    Examples:
      | path                                          | host          | port |
      | /https://api.talon.one/v2/customer_sessions/1 | api.talon.one | 443  |
      | /http://api.talon.one/v2/customer_sessions/1  | api.talon.one | 443  |
      | /https://api.talon.one:8443/v2/x              | api.talon.one | 8443 |
      | /HTTPS://API.TALON.ONE/v2/x                   | api.talon.one | 443  |

  Scenario: What the vendor receives
    Given an app call with Proxy-Authorization, Authorization, Connection and a JSON body
    When the proxy forwards it to "/https://api.talon.one/v2/customer_sessions/sess-1?dry=true&b=%2F"
    Then the method, path, query and body reach the vendor unchanged
    And Host is set to the vendor host from the path
    And Proxy-Authorization is removed
    And Authorization and every other end-to-end header are kept
    And headers for one hop only, such as Connection and Proxy-Connection, are removed as HTTP requires
    And the proxy adds no header of its own, such as Accept-Encoding or User-Agent

  Scenario: A port in the path reaches the vendor's Host header
    When an app sends "GET /https://api.talon.one:8443/v2/x" with "Host: localhost:8080"
    Then the vendor gets "Host: api.talon.one:8443"

  Scenario: An address with no path calls the vendor's root
    When an app sends "GET /https://api.talon.one" with "Host: localhost:8080"
    Then the vendor gets the path /

  Scenario Outline: Choosing http or https to the vendor
    Given <settings>
    When an app sends "GET <path>" with "Host: localhost:8080"
    Then the proxy calls the vendor over <scheme> on port <port>

    Examples:
      | settings                                     | path                         | scheme | port |
      | an api with TLS origination on, the default  | /https://api.talon.one/v2/x  | https  | 443  |
      | an api with TLS origination on, the default  | /http://api.talon.one/v2/x   | https  | 443  |
      | an api with TLS origination on, the default  | /http://api.talon.one:8080/x | https  | 8080 |
      | an api with tlsOrigination set to false      | /http://api.talon.one/v2/x   | http   | 80   |
      | a not-passed call with the built-in settings | /http://api.x.com/a          | https  | 443  |
      | a not-passed call with the built-in settings | /https://api.x.com/a         | https  | 443  |

  Scenario: The record of a vendor URL in path call
    When an app sends "PUT /https://api.talon.one/v2/customer_sessions/sess-1" with "Host: localhost:8080"
    Then the record says hotline.ingress url_in_path
    And url.full is "https://api.talon.one/v2/customer_sessions/REDACTED"
    And server.address is api.talon.one and server.port is 443
    And nothing in the record names the proxy's own address

  Scenario: A not-passed call gets the minimal record
    Given a not-passed call with the built-in settings
    When an app sends "GET /https://api.x.com/a" with "Host: localhost:8080"
    Then the record holds only hotline.integration.id, hotline.ingress, client.address, server.address, network.peer.address, http.request.method, http.response.status_code, http.request.body.size, http.response.body.size, error.type and hotline.timing.total_us
    And the record's ID is not-passed
    And the record says hotline.ingress url_in_path

  Scenario Outline: Damaged addresses are refused
    When an app sends "GET <path>" with "Host: localhost:8080"
    Then the proxy answers 400 with "Proxy-Status: hotline; error=http_request_error"
    And nothing is sent to any vendor
    And the record is minimal with hotline.ingress url_in_path, http.response.status_code 400 and error.type http_request_error
    And the record has no server.address

    Examples:
      | path                                       |
      | /https:/api.talon.one/v2/x                 |
      | /https%3A%2F%2Fapi.talon.one/v2/x          |
      | /https:///v2/no-host                       |
      | /HTTPS:/api.talon.one/v2/x                 |

  Scenario: A refused call keeps its integration ID
    When an app sends "GET /https:/api.talon.one/v2/x" with "Host: localhost:8080"
    Then the proxy answers 400
    And the record's ID is talon-shop-a
    And hotline.requests goes up by one

  Scenario: A call over TLS to the proxy
    Given the proxy port uses TLS with a certificate the app trusts
    When the app sends "GET /https://api.talon.one/v2/x" over TLS to the proxy
    Then the vendor gets the call over https
    And the record says hotline.ingress url_in_path

