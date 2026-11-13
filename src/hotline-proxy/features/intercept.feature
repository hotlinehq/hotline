Feature: The intercept ingress
  A program opens a secure connection to a vendor. A firewall on the same
  machine sends it to the proxy's transparent listener port, and the program
  never knows. There is no CONNECT, so the proxy reads the vendor's name
  from the first message of the TLS handshake (the site name, called the
  SNI), shows a certificate for it signed by the customer's issuer, and
  handles each call inside like any other call, with hotline.ingress
  intercept (Peter, 2026-11-08).

  The tests need no firewall: a redirected connection looks the same as an
  app that connects straight to the listener and names the vendor in the
  handshake, so the test app does that.

  Background:
    Given an issuer named customer in certs
    And the transparent listener picks it with integration "ci-lint"
    And an app that trusts the interception issuer

  Rule: Calls on the listener are handled like any other call

    Scenario: A call reaches the vendor unchanged
      When the app connects to the listener naming api.talon.one in the handshake
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
      And the record says hotline.ingress transparent and ID ci-lint

    Scenario: The certificate names the site in the handshake
      When the app connects to the listener naming api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the interception issuer
      And it is valid for 24 hours

    Scenario: A host's certificate is made once and reused
      When the app opens two connections to the listener naming api.stripe.com
      Then both tunnels show the same certificate

    Scenario: Every host is intercepted
      When the app connects to the listener naming example.org in the handshake
      And the app sends its call with the header "Host: example.org"
      Then the test vendor for example.org got the call
      And the record says hotline.ingress transparent and ID ci-lint

    Scenario: Without an integration the ID is not-passed
      Given the transparent listener picks it
      When the app connects to the listener naming example.org in the handshake
      And the app sends its call with the header "Host: example.org"
      Then the record says hotline.ingress transparent and ID not-passed

    Scenario: A call for another host is refused
      When the app connects to the listener naming api.talon.one in the handshake
      And the app sends its call with the header "Host: api.stripe.com"
      Then the proxy answers 421 with a Proxy-Status header
      And nothing is sent to any vendor
      And the record is minimal with hotline.ingress transparent, http.response.status_code 421 and error.type http_request_error

    Scenario: Proxy-Authorization inside the call is ignored
      When the app connects to the listener naming api.talon.one in the handshake
      And the app sends its call with Proxy-Authorization for talon-shop-b
      Then the record says hotline.ingress transparent and ID ci-lint
      And the test vendor never saw Proxy-Authorization

    Scenario: The ingress has its own issuer
      Given the connect part picks the issuer named customer
      And a second issuer named other, which the transparent listener picks
      When the app opens a tunnel to api.stripe.com:443 with CONNECT
      And the app connects to the listener naming api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the second issuer

  Rule: A handshake with no site name uses the address the program called

    Scenario: A call by IP address is intercepted with the IP as host
      Given the original destination of the connection is the address of api.talon.one
      When the app connects to the listener with no name in the handshake, calling the address of api.talon.one
      Then the app sees a certificate for the IP api.talon.one signed by the interception issuer
      When the app sends its call
      Then the proxy connected to the address of api.talon.one
      And the record says hotline.ingress transparent and ID ci-lint

    Scenario: No site name and no original destination
      Given the original destination cannot be read
      When the app connects to the listener with no name in the handshake, calling the address of api.talon.one
      Then the connection is closed with no TLS set up
      And the intercept record has hotline.ingress transparent and error.type no_sni

    Scenario: A call by IP address to an internal address is refused
      Given the original destination of the connection is 10.0.0.5:443
      When the app connects to the listener with no name in the handshake, calling 10.0.0.5
      And the app sends its call with the header "Host: 10.0.0.5"
      Then the proxy answers 403 with "Proxy-Status: hotline; error=destination_ip_prohibited"
      And nothing is sent to any vendor

  Rule: Encrypted client hello is answered as the outer name

    Scenario: An ECH handshake is answered as the outer name
      The real site name is encrypted, so the proxy cannot intercept the
      first attempt. It answers as the outer name, with no ECH keys, and a
      client that follows the ECH standard retries without ECH.
      When the app connects to the listener with ECH and the outer name ech.example.net
      Then the app's TLS setup fails because the proxy answered as the outer name ech.example.net and offered no ECH keys
      And the intercept record has hotline.ingress transparent, server.address ech.example.net and error.type ech_rejected
      When the app retries without ECH naming api.talon.one
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged

    Scenario: An outer name in ech.relay is relayed untouched
      Given the transparent listener lists ech.example.net in ech.relay
      When the app connects to the listener with ECH and the outer name ech.example.net
      Then the proxy dialed ech.example.net by its name, never the address the app chose
      When the app closes its connections
      Then the intercept record has hotline.ingress transparent, server.address ech.example.net, hotline.intercept.mode passthrough and hotline.intercept.reason ech

  Rule: Hosts in passthrough are relayed untouched

    Scenario Outline: A listed host is relayed by its name
      Given the transparent listener lists <listed> in passthrough
      And the original destination of the connection is 192.0.2.99:443
      When the app connects to the listener naming <host> in the handshake
      Then the proxy dialed <host> by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the intercept record has hotline.ingress transparent, server.address <host>, hotline.intercept.mode passthrough and hotline.intercept.reason passthrough

      Examples:
        | listed             | host                |
        | pinned.example.com | pinned.example.com  |
        | *.example.com      | deep.pinned.example.com |

    Scenario: A host that is not listed is still intercepted
      Given the transparent listener lists pinned.example.com in passthrough
      When the app connects to the listener naming api.talon.one in the handshake
      Then the app sees a certificate for api.talon.one signed by the interception issuer

  Rule: Connections that cannot be intercepted are recorded

    Scenario: An app that does not trust the issuer
      Given an app that does not trust the interception issuer
      When the app connects to the listener naming api.talon.one in the handshake
      Then TLS inside the tunnel fails to set up
      And nothing is sent to any vendor
      And the intercept record has hotline.integration.id ci-lint, server.address api.talon.one and error.type tls_untrusted_by_app

    Scenario: Plain text on the listener
      When the app sends plain HTTP bytes to the listener
      Then the connection is closed with no TLS set up
      And the intercept record has hotline.ingress transparent and error.type not_tls

    Scenario Outline: A first message that is not a usable client hello
      When the app sends <bytes> to the listener
      Then the intercept record has hotline.ingress transparent and error.type <error>

      Examples:
        | bytes                                                   | error               |
        | a handshake message that is not a hello                 | not_tls             |
        | a record larger than TLS allows                         | not_tls             |
        | an empty record                                         | not_tls             |
        | a record cut short                                      | not_tls             |
        | a hello that ends after its random bytes                | not_tls             |
        | a hello cut inside its session ID                       | not_tls             |
        | a hello whose extension overruns it                     | not_tls             |
        | a hello with no extensions                              | no_sni              |
        | a hello with a site name too short to read              | no_sni              |
        | a hello with a site name longer than its extension      | no_sni              |
        | a hello with an extensions length that stops early      | tls_handshake_failed |

  Rule: Relays and the listener

    Scenario Outline: A relay that cannot reach its host is recorded
      Given the transparent listener lists pinned.example.com in passthrough
      And <host setup>
      When the app connects to the listener naming pinned.example.com in the handshake
      Then the app's TLS setup fails
      And the intercept record has hotline.ingress transparent, server.address pinned.example.com, hotline.intercept.mode passthrough and error.type <error>
      And the proxy opens no connection

      Examples:
        | host setup                                         | error                     |
        | the vendor name pinned.example.com resolves to 10.0.0.5 | destination_ip_prohibited |
        | the name pinned.example.com has no address         | destination_unavailable   |

    Scenario: Stopping the proxy ends a relay
      Given the transparent listener lists pinned.example.com in passthrough
      When the app connects to the listener naming pinned.example.com in the handshake
      And the proxy stops
      Then the intercept record has hotline.ingress transparent, server.address pinned.example.com and hotline.intercept.mode passthrough

    Scenario: A listener the config no longer has keeps working until a restart
      Given the app has connected to the listener naming api.stripe.com in the handshake
      When the config file drops the transparent listener
      And the proxy checks its files
      And the app connects to the listener naming api.stripe.com in the handshake
      Then the proxy logs a warning that listener intercept needs a restart
      And the app sees a certificate for api.stripe.com signed by the interception issuer

  Rule: The listener and its issuer

    Scenario: A renewed issuer is used without a restart
      Given the app has connected to the listener naming api.stripe.com in the handshake
      When the files of the issuer named customer are replaced with a second issuer
      And the proxy checks its files
      And the app connects to the listener naming api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the second issuer

    Scenario: The address is already taken
      Given another program listens on the transparent listener address
      When the proxy loads the config
      Then the proxy does not start, and says the address is in use

  Rule: The config

    Scenario Outline: A transparent listener that cannot work stops the proxy at start
      Given <problem>
      When the proxy loads the config
      Then the config is refused with a message naming the problem

      Examples:
        | problem                                              |
        | the transparent listener names a cert that is not in certs   |
        | the transparent listener names no cert                       |
        | the transparent listener has no address                      |
        | the transparent listener lists an ech.relay name that is not a host name |
        | the transparent listener lists a passthrough pattern that is not a name or a *. suffix |
