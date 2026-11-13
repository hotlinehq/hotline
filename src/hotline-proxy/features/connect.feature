Feature: The CONNECT ingress, with interception
  The app keeps its normal https address and only sets the proxy, for
  example with HTTPS_PROXY. It sends CONNECT host:port, with the integration
  ID in Proxy-Authorization, and the proxy answers 200. The app then sets up
  TLS inside the tunnel, and the proxy answers it itself, posing as the
  vendor: it shows a certificate for that host, made on the fly and signed
  by the customer's own issuer. The issuer is written once in the certs section, under a name, with two PEM files; the connect part picks it with cert: <name> (Peter, 2026-11-08). Every
  call inside the tunnel is then handled like a call on any other ingress,
  with hotline.ingress connect (Peter, 2026-10-29).

  Background:
    Given an interception issuer named customer in certs, which the connect part picks
    And an app that trusts the interception issuer

  Scenario: A call through a tunnel reaches the vendor unchanged
    When the app calls https://api.talon.one through the proxy as integration talon-shop-a
    Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
    And the test vendor never saw Proxy-Authorization
    And the app gets the vendor's status, headers and body unchanged
    And the record says hotline.ingress connect and ID talon-shop-a
    And the record has url.template /v2/customer_sessions/{customerSessionId}
    And client.address is the app's IP address

  Scenario: A signed call stays valid
    Signed calls, such as AWS calls, are checked by the vendor against the
    exact Host, path, query, body and signed headers the app sent.
    When the app sends a signed call to https://api.talon.one through a tunnel
    Then the test vendor got the same Host, path, query, body and signed headers the app signed

  Scenario: The proxy poses as the host the tunnel names
    When the app opens a tunnel to api.stripe.com:443
    Then the app sees a certificate for api.stripe.com signed by the interception issuer
    And it is valid for 24 hours

  Scenario: A host's certificate is made once and reused
    When the app opens two tunnels to api.stripe.com:443
    Then both tunnels show the same certificate

  Scenario: Every host is intercepted
    When the app calls https://example.org through the proxy with no ID
    Then the test vendor for example.org got the call
    And the record says hotline.ingress connect and ID not-passed

  Scenario: The integration ID comes from the CONNECT request
    When the app opens a tunnel to api.talon.one:443 as integration talon-shop-a
    And sends a call inside it with Proxy-Authorization for talon-shop-b
    Then the record says hotline.ingress connect and ID talon-shop-a
    And the test vendor never saw Proxy-Authorization

  Scenario: A call for another host inside a tunnel is refused
    When the app opens a tunnel to api.talon.one:443 as integration talon-shop-a
    And sends a call inside it with the header "Host: api.stripe.com"
    Then the proxy answers 421 with a Proxy-Status header
    And nothing is sent to any vendor
    And the record is minimal with hotline.ingress connect, http.response.status_code 421 and error.type http_request_error
    And the record has no body sizes

  Scenario Outline: HTTP versions inside the tunnel
    Given the app offers only <version> inside the tunnel
    When the app calls https://api.talon.one through the proxy as integration talon-shop-a
    Then the call inside the tunnel used <version>
    And the record says hotline.ingress connect and ID talon-shop-a

    Examples:
      | version  |
      | HTTP/1.1 |
      | HTTP/2   |

  Scenario: Plain HTTP inside a tunnel
    Some clients also tunnel plain http calls, such as WebSocket libraries
    for ws:// addresses. The proxy tells them from TLS by the first byte and
    handles them like a plain http call on the forward proxy ingress, with
    TLS origination (Peter, 2026-10-29).
    When the app opens a tunnel to api.talon.one:80 as integration talon-shop-a
    And sends a plain HTTP call inside it
    Then the test vendor got the call over https on port 443
    And the record says hotline.ingress connect and ID talon-shop-a

  Scenario: The proxy's handshake with the app is timed once per tunnel
    The proxy's own cost includes setting up TLS with the app, and making
    the host's certificate when needed (Peter, 2026-10-29).
    When the app sends two calls through one tunnel
    Then the first call's trace has an "app tls handshake" span at the start of "hotline call"
    And the second call's trace has no "app tls handshake" span

  Scenario: An app that does not trust the interception issuer
    The proxy has read the CONNECT, so it knows the integration and the
    host, and the record tells which app is missing the issuer.
    Given an app that does not trust the interception issuer
    When the app opens a tunnel to api.talon.one:443 as integration talon-shop-a
    Then TLS inside the tunnel fails to set up
    And nothing is sent to any vendor
    And the record is minimal with hotline.integration.id talon-shop-a, server.address api.talon.one and error.type tls_untrusted_by_app

  Scenario: The guard against internal addresses still applies
    When the app calls https://10.0.0.5 through the proxy as integration talon-shop-a
    Then the proxy answers 403 with "Proxy-Status: hotline; error=destination_ip_prohibited"
    And nothing is sent to any vendor

  Scenario: CONNECT over a TLS proxy port
    The app reaches the proxy with an https proxy address, so the CONNECT
    request and the integration ID travel encrypted too.
    Given a proxy certificate for the name hotline-proxy from the test issuer
    And a config file whose tls part points to its cert and key files
    When the app calls https://api.talon.one through an https proxy address as integration talon-shop-a
    Then the app gets the vendor's status, headers and body unchanged
    And the record says hotline.ingress connect and ID talon-shop-a

  Scenario Outline: A CONNECT the proxy refuses
    When an app sends "CONNECT <target>"
    Then the proxy answers 400
    And nothing is sent to any vendor
    And the record is minimal with hotline.ingress connect, http.response.status_code 400 and error.type http_request_error

    Examples:
      | target         |
      | api.talon.one  |
      | :443           |

  Scenario: CONNECT over HTTP/2
    Over HTTP/2 a tunnel is one stream of a shared connection: the app's
    bytes travel in the stream's request body and the proxy's in its reply.
    Inside, it works like a tunnel opened over HTTP/1.1 (Peter, 2026-10-30).
    When the app opens a tunnel to api.talon.one:443 as integration talon-shop-a over HTTP/2
    And sends a call inside it with Proxy-Authorization for talon-shop-b
    Then the app gets the vendor's status, headers and body unchanged
    And the record says hotline.ingress connect and ID talon-shop-a
    And client.address is the app's IP address
    And the test vendor never saw Proxy-Authorization

  Scenario: Many tunnels share one HTTP/2 connection
    When the app opens two tunnels to api.stripe.com:443 over one HTTP/2 connection
    Then both tunnels show the same certificate

  Scenario: Plain HTTP inside a tunnel opened over HTTP/2
    When the app opens a tunnel to api.talon.one:80 as integration talon-shop-a over HTTP/2
    And sends a plain HTTP call inside it
    Then the test vendor got the call over https on port 443
    And the record says hotline.ingress connect and ID talon-shop-a

  Scenario: A CONNECT over HTTP/2 the proxy refuses
    When an app sends "CONNECT api.talon.one" over plain HTTP/2
    Then the proxy answers 400
    And nothing is sent to any vendor
    And the record is minimal with hotline.ingress connect, http.response.status_code 400 and error.type http_request_error

  Scenario Outline: Stopping the proxy closes a tunnel the app has not used yet
    Given the app has opened a tunnel to api.talon.one:80 <over> and sent nothing in it
    When the proxy stops
    Then the tunnel is closed

    Examples:
      | over          |
      | over HTTP/1.1 |
      | over HTTP/2   |

  Scenario: Stopping the proxy closes an HTTP/2 tunnel after its calls
    The proxy lets calls in flight finish, then closes the tunnel, which
    ends its stream, so the HTTP/2 connection can close too.
    Given the app has opened a tunnel to api.talon.one:443 as integration talon-shop-a over HTTP/2
    And sends a call inside it with the header "Host: api.talon.one"
    When the proxy stops
    Then the tunnel is closed

  Scenario: A renewed issuer is used without a restart
    Given the app has opened a tunnel to api.stripe.com:443
    When the files of the issuer named customer are replaced with a second issuer
    And the proxy checks its files
    And the app opens a tunnel to api.stripe.com:443
    Then the app sees a certificate for api.stripe.com signed by the second issuer

  Scenario Outline: A bad issuer stops the proxy at start
    Given <problem>
    When the proxy loads the config
    Then the config is refused with a message naming the problem

    Examples:
      | problem                                                  |
      | the connect part cert is not an issuer certificate     |
      | the connect part key does not belong to its certificate |
      | the connect part issuer has already expired            |
      | the connect part has a cert file but no key file           |
      | the connect part names the cert "missing", which is not in certs |
      | an issuer is written with no files                       |
      | the connect part names no cert                           |

  Scenario: A cert nothing signs with need not be a certificate authority
    Whether a cert may sign is checked where it is used (Peter, 2026-11-13).
    Given a cert that nothing signs with is not a certificate authority
    When the proxy loads the config
    Then it is accepted

  Scenario: An issuer is written once and used by name
    Given an interception issuer named customer in certs, which the connect part picks
    When the proxy starts
    Then the proxy is running

  Scenario: An issuer valid for more than a year gets a warning
    Given an interception issuer valid for two years
    When the proxy starts
    Then the proxy logs a warning that the interception issuer is valid for more than a year
