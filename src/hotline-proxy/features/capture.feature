Feature: The capture ingress
  On a Mac, a small helper (a system extension, built in its own project,
  hotline-macos-capture) picks out the connections of the apps named in the
  --capture flag and hands each one to the proxy port as a CONNECT to where
  the app really called, with three headers: Hotline-Capture with the
  version, and the process number and program path of the app. The tunnel
  then carries the app's own bytes, so the proxy serves it like an
  intercepted connection: it reads the site name from the TLS handshake,
  shows a certificate for it signed by the listen.connect issuer, and handles
  each call inside like any other call, with hotline.ingress capture
  (Peter, 2026-11-10).

  The proxy believes the capture headers only from this machine. From
  anywhere else the CONNECT is an ordinary one. The tests need no Mac: the
  test extension is a program that sends the same CONNECT.

  Background:
    Given an interception issuer named customer in issuers, which listen.connect picks
    And an app that trusts the interception issuer

  Rule: Captured calls are handled like any other call

    Scenario: A call reaches the vendor unchanged, and the record names the program
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call
      Then the test vendor got the call over https with its Host, path, query, headers and body unchanged
      And the record says hotline.ingress capture and ID not-passed
      And the record says the program is process 4242 at "/usr/bin/curl"

    Scenario: The extension names the address, and the certificate names the site in the handshake
      When the extension opens a captured tunnel to the address of api.stripe.com:443 for process 4242 at "/usr/bin/curl" and the app names api.stripe.com in the handshake
      Then the app sees a certificate for api.stripe.com signed by the interception issuer

    Scenario: The port in the CONNECT decides where the proxy connects
      When the extension opens a captured tunnel to api.talon.one:8443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      And the app sends its call with the header "Host: api.talon.one:8443"
      Then the test vendor got the call over https on port 8443

    Scenario: A handshake with no site name uses the address in the CONNECT
      When the extension opens a captured tunnel to the address of api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names no site in the handshake
      Then the app sees a certificate for the IP api.talon.one signed by the interception issuer
      When the app sends its call
      Then the proxy connected to the address of api.talon.one
      And the record says hotline.ingress capture and ID not-passed

  Rule: Hosts in listen.connect passthrough are relayed untouched

    Scenario: A captured call to a listed host is relayed by its name
      Given listen.connect lists pinned.example.com in passthrough
      When the extension opens a captured tunnel to the address of pinned.example.com:443 for process 4242 at "/usr/bin/curl" and the app names pinned.example.com in the handshake
      Then the proxy dialed pinned.example.com by its name, never the address the app chose
      And the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress capture, server.address pinned.example.com, hotline.intercept.mode passthrough, hotline.intercept.reason passthrough, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: An ordinary tunnel to a listed host is relayed too
      Given listen.connect lists *.example.com in passthrough
      And the app also trusts the vendors' own issuer
      When the app opens a tunnel to pinned.example.com:443
      Then the vendor's own certificate reaches the app, not one from the interception issuer
      When the app closes its connections
      Then the capture record has hotline.ingress connect, server.address pinned.example.com, hotline.intercept.mode passthrough and hotline.intercept.reason passthrough

    Scenario: A passthrough pattern that is not a name is refused
      Given listen.connect lists a passthrough pattern that is not a name or a *. suffix
      When the proxy loads the config
      Then the config is refused with a message naming the problem

  Rule: Connections that cannot be intercepted are recorded with the program

    Scenario: An app that does not trust the issuer
      Given an app that does not trust the interception issuer
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and the app names api.talon.one in the handshake
      Then TLS inside the tunnel fails to set up
      And nothing is sent to any vendor
      And the capture record has hotline.integration.id not-passed, server.address api.talon.one, error.type tls_untrusted_by_app, process.pid 4242 and process.executable.path /usr/bin/curl

    Scenario: Plain text in a captured tunnel
      When the extension opens a captured tunnel to api.talon.one:443 for process 4242 at "/usr/bin/curl" and sends plain HTTP bytes
      Then the capture record has hotline.ingress capture, error.type not_tls and process.pid 4242

  Rule: The capture headers are not trusted

    Scenario Outline: Capture headers that are not usable are refused
      When the extension sends a CONNECT to api.talon.one:443 with <headers>
      Then the proxy answers 400 with a Proxy-Status header
      And the record says hotline.ingress capture and ID not-passed

      Examples:
        | headers                                |
        | an unknown capture version             |
        | a process number that is not a number  |
        | process number 0                       |
        | the capture header twice               |
        | two process numbers                    |
        | a program path over 4096 bytes         |

    Scenario: Capture headers from another machine are ignored
      Given the proxy listens on all interfaces
      When a program on another machine opens a tunnel to api.talon.one:443 with the capture headers for process 4242 at "/usr/bin/curl"
      And the app sends its call
      Then the record says hotline.ingress connect and ID not-passed
      And the record names no program

  Rule: The extension's check that the proxy is there leaves no trace

    Scenario: A connection that sends nothing makes no record
      When a program opens a connection to the proxy port and closes it without sending anything
      Then no record is sent
